package tui

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/njahangirov/kube-console/internal/kube"
)

// KV_LIVE=<context> go test -run Drive ./internal/tui -v
// Drives the real model (keys in, View() out) against a cluster, read-only.
func TestDriveLive(t *testing.T) {
	name := os.Getenv("KV_LIVE")
	if name == "" {
		t.Skip("set KV_LIVE")
	}
	mgr, err := kube.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	m := New(mgr, "pods", name)
	m.Update(tea.WindowSizeMsg{Width: 170, Height: 60})
	run := func(cmd tea.Cmd, wait time.Duration) {
		// Execute a command and feed what it returns back in, breadth-first, until
		// nothing is left or the wait is over. Ticks are dropped.
		q := []tea.Cmd{cmd}
		deadline := time.Now().Add(wait)
		for len(q) > 0 && time.Now().Before(deadline) {
			c := q[0]
			q = q[1:]
			if c == nil {
				continue
			}
			ch := make(chan tea.Msg, 1)
			go func() { ch <- c() }()
			var msg tea.Msg
			select {
			case msg = <-ch:
			case <-time.After(time.Until(deadline)):
				return
			}
			switch v := msg.(type) {
			case nil, tickMsg:
				continue
			case tea.BatchMsg:
				q = append(q, v...)
				continue
			}
			if _, ok := msg.(tea.KeyMsg); ok {
				continue
			}
			_, next := m.Update(msg)
			q = append(q, next)
		}
	}
	key := func(s string) tea.Cmd {
		var k tea.KeyMsg
		switch s {
		case "enter":
			k = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			k = tea.KeyMsg{Type: tea.KeyEsc}
		default:
			k = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
		}
		_, c := m.Update(k)
		return c
	}
	command := func(line string, wait time.Duration) string {
		key(":")
		for _, r := range line {
			m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		}
		run(key("enter"), wait)
		return stripANSI(m.View())
	}
	screen := func(label string) string {
		v := stripANSI(m.View())
		t.Logf("---- %s\n%s", label, v)
		return v
	}
	run(m.Init(), 30*time.Second)
	screen("start")

	run(key("T"), 60*time.Second)
	if v := screen("T = Cluster Topology"); !strings.Contains(v, "NODE POOLS") || !strings.Contains(v, "Cluster Topology") {
		t.Error("T did not open the cluster topology")
	}
	key("esc")
	if v := command("helm", 60*time.Second); !strings.Contains(v, "Helm releases") {
		t.Errorf("helm:\n%s", v)
	}
	screen(":helm")
	key("esc")
	if v := command("helm argocd history", 60*time.Second); !strings.Contains(v, "REV") {
		t.Errorf("helm history:\n%s", v)
	}
	screen(":helm argocd history")
	key("esc")
	if v := command("helm charts", 60*time.Second); !strings.Contains(v, "Helm charts") {
		t.Errorf("charts:\n%s", v)
	}
	key("esc")
	if v := command("topology argocd", 60*time.Second); !strings.Contains(v, "Namespace Topology") || !strings.Contains(v, "└─") {
		t.Errorf("ns topology:\n%s", v)
	}
	screen(":topology argocd")
	key("esc")
	command("ns argocd", 20*time.Second)
	command("deploy", 20*time.Second)
	screen("deployments in argocd")
	run(key("l"), 12*time.Second)
	if v := screen("l on a deployment = merged logs"); !strings.Contains(v, "Logs") || !strings.Contains(v, "pod") {
		t.Errorf("merged logs not shown:\n%s", v)
	}
	key("esc")
	m.stopLogs()
	command("pods", 20*time.Second)
	if v := command("ls /", 60*time.Second); !strings.Contains(v, "MODIFIED") && !strings.Contains(v, "tar/sh") {
		t.Errorf("ls:\n%s", v)
	}
	screen(":ls /")
	key("esc")
	if v := command("topology helm", 60*time.Second); !strings.Contains(v, "Helm Topology") || !strings.Contains(v, "releases") {
		t.Errorf("helm topology:\n%s", v)
	}
	screen(":topology helm")
	key("esc")
	command("ns argocd", 20*time.Second)
	command("pods", 20*time.Second)
	if v := screen("pods with usage"); !strings.Contains(v, "CPU") || !strings.Contains(v, "MEMORY") || !strings.Contains(v, "Mi ") {
		t.Errorf("pod usage columns missing:\n%s", v)
	}
	// :create opens $EDITOR; with EDITOR=true it returns unchanged -> "nothing created".
	os.Setenv("EDITOR", "true")
	c := key("N")
	if c == nil {
		t.Error("N returned no command")
	}
	m.Update(createdEditMsg{path: writeTemp(t, "x"), original: "x", ns: "argocd"})
	if !strings.Contains(m.status, "nothing created") {
		t.Errorf("unchanged create should cancel, status %q", m.status)
	}
	// A real dry run through validateAndCreate, then cancel at the confirmation.
	run(m.validateAndCreate(kube.TemplateFor(kube.Kind{Name: "configmaps", Kind: "ConfigMap", Version: "v1", Namespaced: true}, "argocd"), "argocd"), 30*time.Second)
	if m.purpose != inConfirm {
		t.Errorf("expected the create confirmation prompt, status %q", m.status)
	}
	screen("create: dry run passed, asking to confirm")
	run(key("esc"), time.Second)
	if m.confirm != nil && m.purpose == inConfirm {
		t.Error("esc should cancel the confirmation")
	}
}

func writeTemp(t *testing.T, s string) string {
	f, err := os.CreateTemp(t.TempDir(), "x*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(s)
	f.Close()
	return f.Name()
}
