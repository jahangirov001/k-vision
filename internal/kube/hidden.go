package kube

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Hiding a cluster removes it from K-Vision's lists only. The kubeconfig is not
// touched, so kubectl and every other tool still see the cluster, and Show brings it
// back. The list lives in ~/.k-vision/settings.json.

type settings struct {
	Hidden []string `json:"hiddenContexts"`
}

func settingsPath() string { return filepath.Join(filepath.Dir(AuditPath()), "settings.json") }

func loadHidden() map[string]bool {
	out := map[string]bool{}
	data, err := os.ReadFile(settingsPath())
	if err != nil {
		return out
	}
	var s settings
	if json.Unmarshal(data, &s) == nil {
		for _, n := range s.Hidden {
			out[n] = true
		}
	}
	return out
}

func saveHidden(h map[string]bool) error {
	s := settings{Hidden: make([]string, 0, len(h))}
	for n := range h {
		s.Hidden = append(s.Hidden, n)
	}
	sort.Strings(s.Hidden)
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	p := settingsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, append(data, '\n'), 0o600)
}

// SetHidden hides a context from K-Vision's lists, or shows it again.
func (m *Manager) SetHidden(name string, hide bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.raw.Contexts[name]; !ok {
		return fmt.Errorf("no context %q in kubeconfig", name)
	}
	if hide {
		m.hidden[name] = true
	} else {
		delete(m.hidden, name)
	}
	return saveHidden(m.hidden)
}

// HiddenContexts lists the hidden contexts, for the "Show hidden" menu.
func (m *Manager) HiddenContexts() []ContextInfo {
	var out []ContextInfo
	for _, c := range m.AllContexts() {
		if c.Hidden {
			out = append(out, c)
		}
	}
	return out
}
