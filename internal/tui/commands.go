package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/njahangirov/kube-console/internal/kube"
)

// Command is one `:` command. The same table drives the autocomplete under the
// prompt, the help screen, and `kv help`, so the three never disagree.
type Command struct {
	Name    string
	Aliases []string
	Args    string
	Does    string
	Example string
}

var Commands = []Command{
	{"pods", []string{"po"}, "[filter]", "List pods, optionally already filtered", ":pods restarts>0"},
	{"deploy", []string{"deployments"}, "[filter]", "List deployments", ":deploy !ok"},
	{"nodes", []string{"no"}, "[filter]", "List nodes", ":nodes status:schedulingdisabled"},
	{"app", []string{"applications"}, "[filter]", "ArgoCD applications", ":app sync:outofsync"},
	{"nodepools", nil, "", "Karpenter NodePools", ":nodepools"},
	{"events", []string{"ev"}, "[filter]", "Events, newest problems first", ":events type:warning age<1h"},
	{"<any kind>", nil, "[filter]", "Any resource type or CRD by name, short name or kind", ":scaledobjects"},
	{"all", []string{"everywhere"}, "<kind> [filter]", "One kind in EVERY cluster at once", ":all pods !ok"},
	{"ctx", []string{"context"}, "[name]", "Switch cluster; partial names work. No name opens the picker", ":ctx payments-dev"},
	{"ns", []string{"namespace"}, "<name>|all", "Show one namespace, or all", ":ns shop"},
	{"overview", []string{"get-all"}, "<namespace>", "Namespace Overview: every resource in a namespace, grouped by kind", ":overview shop"},
	{"topology", []string{"topo"}, "[ns|helm]", "Cluster Topology (pools ↔ namespaces); <ns>: its Ingress→Service→workload tree; helm: which release created what. +system adds platform namespaces", ":topology shop"},
	{"helm", nil, "[charts|<release>]", "Helm releases or charts; a release: summary, then values · all · manifest · notes · history [rev]", ":helm shop-api values"},
	{"warnings", []string{"warn", "problems"}, "[15m|1h|6h|24h]", "Cluster warnings: Warning events grouped by problem, broken first, with node pool and examples", ":warnings 6h"},
	{"argo", []string{"argocd"}, "", "Argo CD dashboard: app health and sync, what needs a look, recent syncs, by project", ":argo"},
	{"cost", nil, "[7d] [workload]", "Spend from OpenCost: by namespace (or workload/pod/node/team), idle, and what right-sizing would save", ":cost 30d workload"},
	{"create", []string{"new"}, "[kind|-f file]", "Create from YAML: a template opens in $EDITOR; checked by a server dry run before it is created", ":create deployment"},
	{"ls", []string{"files"}, "[dir]", "List a directory in the selected pod or node", ":ls /app"},
	{"cp", []string{"copy"}, "<path>|put <file>", "Copy a file or folder from the selected pod/node to ~/Downloads; put uploads (to /tmp unless a dir follows)", ":cp /app/config.yaml"},
	{"logs", nil, "", "Logs; on a workload, every pod of it merged", ":logs"},
	{"kinds", nil, "[search]", "Every resource type this cluster serves", ":kinds karpenter"},
	{"pf", []string{"forwards"}, "", "Running port-forwards", ":pf"},
	{"audit", nil, "", "Every change made from this app", ":audit"},
	{"help", []string{"h", "?"}, "", "This guide", ":help"},
	{"quit", []string{"q", "exit"}, "", "Quit", ":q"},
}

// Key is one key binding, grouped for the help screen.
type Key struct{ Group, Keys, Does string }

var Keys = []Key{
	{"Move", "j k  ↑ ↓", "Up and down"},
	{"Move", "g G", "Top and bottom"},
	{"Move", "PgUp PgDn", "A page at a time"},
	{"Move", "1 2 3 4", "Health: all, failing, warning, healthy"},
	{"Find", ":", "Command, with autocomplete (Tab)"},
	{"Find", "/", "Filter as you type"},
	{"Find", "Esc", "Clear the filter, or go back"},
	{"Find", "n  0", "This row's namespace / all namespaces"},
	{"Find", "C", "Cluster picker: h removes from the list (kubeconfig untouched), d deletes from kubeconfig"},
	{"Look", "Enter  y", "YAML"},
	{"Look", "x", "Why is this pod unhealthy"},
	{"Look", "l", "Logs, following. On a workload, or with pods marked: all of them merged"},
	{"Look", "p", "Logs of the previous, crashed container"},
	{"Look", "v", "Decoded values of a secret or configmap"},
	{"Look", "T", "Cluster Topology"},
	{"Look", "H", "Helm releases"},
	{"Change", "s", "Shell: into a pod, or onto a node as root (helper pod, removed on exit)"},
	{"Change", "e", "Edit YAML in $EDITOR, applied after confirmation"},
	{"Change", "N", "Create a resource from YAML (template in $EDITOR)"},
	{"Change", "S", "Scale"},
	{"Change", "r", "Restart: rollout restart a workload, or recreate a pod"},
	{"Change", "f", "Port-forward"},
	{"Change", "c  D", "Cordon or uncordon / drain a node"},
	{"Change", "Space", "Mark a row (Shift-click range in the window); Ctrl+A marks all shown"},
	{"Change", "Ctrl+D", "Delete the row, or every marked row"},
	{"App", "R", "Refresh now"},
	{"App", "?", "This guide"},
	{"App", "q  Ctrl+C", "Quit"},
}

// FilterHelp is the filter language, for the help screen and `kv help`.
var FilterHelp = [][2]string{
	{"repo", "name, namespace, status or any column contains it"},
	{"-kube-system", "a leading minus excludes"},
	{`"two words"`, "quotes keep a phrase together"},
	{"!ok", "only unhealthy rows"},
	{"is:failing", "health: healthy, warning, failing, unhealthy"},
	{"ns:shop", "field contains: name, ns, status, ctx, or any column"},
	{"status:crash|error", "| means or"},
	{"restarts>3", "numeric compare: > < >= <="},
	{"age<1h  age>7d", "age, units s m h d w"},
	{"app=web  tier!=db", "label equals / differs"},
	{"label:team", "label exists"},
}

// complete returns the suggestions for what has been typed after ":".
func (m *Model) complete(line string) []suggestion {
	fields := strings.Fields(line)
	trailing := strings.HasSuffix(line, " ")
	var out []suggestion
	// First word: commands, then resource kinds.
	if len(fields) == 0 || (len(fields) == 1 && !trailing) {
		prefix := ""
		if len(fields) == 1 {
			prefix = strings.ToLower(fields[0])
		}
		for _, c := range Commands {
			if strings.HasPrefix(c.Name, "<") {
				continue
			}
			names := append([]string{c.Name}, c.Aliases...)
			for _, n := range names {
				if strings.HasPrefix(n, prefix) {
					out = append(out, suggestion{text: c.Name, hint: c.Does, args: c.Args})
					break
				}
			}
		}
		if prefix != "" && m.cluster != nil {
			if kinds, err := m.cluster.Kinds(); err == nil {
				seen := map[string]bool{}
				for _, s := range out {
					seen[s.text] = true
				}
				for _, k := range kinds {
					id := k.ID()
					hit := strings.HasPrefix(k.Name, prefix) || strings.HasPrefix(strings.ToLower(k.Kind), prefix)
					for _, sn := range k.ShortNames {
						hit = hit || strings.HasPrefix(sn, prefix)
					}
					if hit && !seen[id] {
						seen[id] = true
						out = append(out, suggestion{text: id, hint: k.Kind, args: "[filter]"})
					}
				}
			}
		}
		return limit(out, 8)
	}
	// Second word: depends on the command.
	cmd := strings.ToLower(fields[0])
	arg := ""
	if !trailing {
		arg = strings.ToLower(fields[len(fields)-1])
	}
	switch cmd {
	case "ctx", "context":
		for _, c := range m.mgr.Contexts() {
			if strings.Contains(strings.ToLower(c.Name), arg) {
				hint := "safe"
				if c.Protected {
					hint = ""
				}
				out = append(out, suggestion{text: "ctx " + short(c.Name), hint: hint})
			}
		}
	case "ns", "namespace":
		for _, ns := range m.namespaces() {
			if strings.HasPrefix(ns, arg) {
				out = append(out, suggestion{text: "ns " + ns})
			}
		}
		if strings.HasPrefix("all", arg) {
			out = append(out, suggestion{text: "ns all", hint: "every namespace"})
		}
	case "all", "everywhere":
		if len(fields) <= 2 && m.cluster != nil {
			if kinds, err := m.cluster.Kinds(); err == nil {
				for _, k := range kinds {
					if strings.HasPrefix(k.Name, arg) {
						out = append(out, suggestion{text: "all " + k.Name, hint: k.Kind})
					}
				}
			}
		}
	default:
		// A kind followed by a filter: offer the filter vocabulary.
		for _, f := range FilterHelp {
			first := strings.Fields(f[0])[0]
			if arg == "" || strings.HasPrefix(first, arg) {
				out = append(out, suggestion{text: strings.Join(fields[:len(fields)-boolInt(!trailing)], " ") + " " + first, hint: f[1]})
			}
		}
	}
	return limit(out, 8)
}

type suggestion struct{ text, hint, args string }

func limit(s []suggestion, n int) []suggestion {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (m *Model) namespaces() []string {
	if m.nsCache != nil {
		return m.nsCache
	}
	seen := map[string]bool{}
	for _, r := range m.rows {
		if r.Namespace != "" {
			seen[r.Namespace] = true
		}
	}
	out := make([]string, 0, len(seen))
	for ns := range seen {
		out = append(out, ns)
	}
	sort.Strings(out)
	return out
}

// kindsText lists every resource type, for :kinds.
func kindsText(kinds []kube.Kind, search string) string {
	var b strings.Builder
	search = strings.ToLower(search)
	group := "\x00"
	for _, k := range kinds {
		hay := strings.ToLower(k.ID() + " " + k.Kind + " " + strings.Join(k.ShortNames, " "))
		if search != "" && !strings.Contains(hay, search) {
			continue
		}
		g := k.Group
		if g == "" {
			g = "core"
		}
		if g != group {
			group = g
			b.WriteString("\n" + stSection.Render(g) + "\n")
		}
		short := ""
		if len(k.ShortNames) > 0 {
			short = stDim.Render(" (" + strings.Join(k.ShortNames, ", ") + ")")
		}
		fmt.Fprintf(&b, "  %s%s  %s\n", stCmd.Render(fmt.Sprintf("%-34s", k.ID())), short, stDim.Render(k.Kind))
	}
	return strings.TrimLeft(b.String(), "\n")
}

// Guide renders the full help: the in-app screen and `kv help` both print this.
func Guide(width int) string {
	if width <= 0 || width > 110 {
		width = 110
	}
	var b strings.Builder
	title := lipgloss.NewStyle().Bold(true).Foreground(cAccent).Render("K-Vision")
	b.WriteString(title + stDim.Render("  ·  terminal and desktop Kubernetes console") + "\n\n")

	b.WriteString(stSection.Render("START") + "\n")
	for _, l := range [][2]string{
		{"kv", "terminal UI on your current kubeconfig context"},
		{"kv deploy", "start on deployments (any resource type works)"},
		{"kv --context payments-dev nodes", "start on another cluster"},
		{"kv ui", "desktop window (Lens-style) on 127.0.0.1"},
		{"kv restart", "quit and reopen the K-Vision window (after an update or an SSO login)"},
		{"kv help", "this guide"},
	} {
		fmt.Fprintf(&b, "  %s  %s\n", stCmd.Render(fmt.Sprintf("%-34s", l[0])), stDim.Render(l[1]))
	}

	b.WriteString("\n" + stSection.Render("COMMANDS") + stDim.Render("   press : then type; Tab completes, ↑↓ picks") + "\n")
	for _, c := range Commands {
		name := ":" + c.Name
		if len(c.Aliases) > 0 {
			name += stDim.Render(" ·" + strings.Join(c.Aliases, " ·"))
		}
		left := name
		if c.Args != "" {
			left += " " + stArg.Render(c.Args)
		}
		pad := 34 - lipgloss.Width(left)
		if pad < 1 {
			pad = 1
		}
		fmt.Fprintf(&b, "  %s%s  %s\n", stCmd.Render(left), strings.Repeat(" ", pad), c.Does)
		if c.Example != "" && c.Example != ":"+c.Name && c.Name != "quit" {
			fmt.Fprintf(&b, "  %s  %s\n", strings.Repeat(" ", 34), stExample.Render("e.g. "+c.Example))
		}
	}

	b.WriteString("\n" + stSection.Render("FILTER") + stDim.Render("   press / ; terms combine with AND") + "\n")
	for _, f := range FilterHelp {
		fmt.Fprintf(&b, "  %s  %s\n", stCmd.Render(fmt.Sprintf("%-22s", f[0])), stDim.Render(f[1]))
	}
	fmt.Fprintf(&b, "  %s\n", stExample.Render(`e.g. /ns:shop restarts>0 -status:running`))

	b.WriteString("\n" + stSection.Render("KEYS") + "\n")
	group := ""
	for _, k := range Keys {
		if k.Group != group {
			group = k.Group
			fmt.Fprintf(&b, "  %s\n", stDim.Render(strings.ToLower(group)))
		}
		fmt.Fprintf(&b, "    %s  %s\n", stKey.Render(fmt.Sprintf("%-12s", k.Keys)), k.Does)
	}

	b.WriteString("\n" + stSection.Render("IN LOGS") + "\n")
	for _, k := range [][2]string{{"/", "find"}, {"F", "follow on/off"}, {"P", "previous container"}, {"c", "next container"}, {"w", "save to ~/Downloads"}, {"Esc", "back"}} {
		fmt.Fprintf(&b, "    %s  %s\n", stKey.Render(fmt.Sprintf("%-12s", k[0])), k[1])
	}
	return b.String()
}

// everythingText renders the namespace overview for the terminal.
func everythingText(res []kube.KindRows) string {
	var b strings.Builder
	total := 0
	for _, k := range res {
		total += len(k.Rows)
	}
	fmt.Fprintf(&b, "%s\n", stDim.Render(fmt.Sprintf("%d resources in %d kinds", total, len(res))))
	for _, k := range res {
		if k.Error != "" {
			continue
		}
		fmt.Fprintf(&b, "\n%s %s\n", stSection.Render(k.Kind.Kind), stFaint.Render(fmt.Sprintf("%d · %s", len(k.Rows), k.Kind.ID())))
		for _, r := range k.Rows {
			extra := []string{}
			for _, c := range k.Columns {
				if v := r.Cells[c]; v != "" && v != "-" {
					extra = append(extra, v)
				}
			}
			status := ""
			if r.Status != "" && r.Status != "-" {
				status = statusCell(r.Health, r.Status)
			}
			fmt.Fprintf(&b, "  %-52s %-22s %s %s\n", r.Name, status, stFaint.Render(strings.Join(extra, "  ")), stFaint.Render(r.Age))
		}
	}
	var denied []string
	for _, k := range res {
		if k.Error != "" {
			denied = append(denied, k.Kind.ID())
		}
	}
	if len(denied) > 0 {
		fmt.Fprintf(&b, "\n%s %s\n", stDim.Render("could not list:"), stFaint.Render(strings.Join(denied, ", ")))
	}
	return b.String()
}
