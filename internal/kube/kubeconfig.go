package kube

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"k8s.io/client-go/tools/clientcmd"
)

// DeleteFromKubeconfig removes a context from the kubeconfig file that defines it,
// as Lens's "Delete" and `kubectl config delete-context` do: kubectl loses it too.
//
// Before anything is written, the file is copied to
// ~/.k-vision/kubeconfig-backups/<name>.<time>, so a mistake is one copy back.
// The context's cluster and user entries are removed as well, but only when no
// other context still uses them -- ml-trials shares ml-trials's.
// kubectl's current context is refused: removing it would leave kubectl pointing
// at nothing.
func (m *Manager) DeleteFromKubeconfig(name string) (backup string, err error) {
	a := Audit{Context: name, Action: "delete-from-kubeconfig", Kind: "contexts", Name: name}
	defer func() {
		if backup != "" {
			a.Detail = "backup " + backup
		}
		record(a, err)
	}()
	m.mu.Lock()
	defer m.mu.Unlock()
	if name == m.raw.CurrentContext {
		return "", fmt.Errorf("%s is kubectl's current context; switch to another first (kubectl config use-context …)", name)
	}
	file, err := m.fileWith(name)
	if err != nil {
		return "", err
	}
	cfg, err := clientcmd.LoadFromFile(file)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", file, err)
	}
	ctx, ok := cfg.Contexts[name]
	if !ok {
		return "", fmt.Errorf("%s is not in %s", name, file)
	}
	if backup, err = copyFile(file, filepath.Join(filepath.Dir(AuditPath()), "kubeconfig-backups",
		filepath.Base(file)+"."+time.Now().Format("20060102-150405"))); err != nil {
		return "", fmt.Errorf("could not back up %s, so nothing was changed: %w", file, err)
	}
	delete(cfg.Contexts, name)
	clusterUsed, userUsed := false, false
	for _, other := range cfg.Contexts {
		clusterUsed = clusterUsed || other.Cluster == ctx.Cluster
		userUsed = userUsed || other.AuthInfo == ctx.AuthInfo
	}
	if !clusterUsed {
		delete(cfg.Clusters, ctx.Cluster)
	}
	if !userUsed {
		delete(cfg.AuthInfos, ctx.AuthInfo)
	}
	if err = clientcmd.WriteToFile(*cfg, file); err != nil {
		return backup, fmt.Errorf("writing %s: %w (the original is saved at %s)", file, err, backup)
	}
	delete(m.raw.Contexts, name)
	delete(m.clusters, name)
	if m.hidden[name] {
		delete(m.hidden, name)
		_ = saveHidden(m.hidden)
	}
	return backup, nil
}

// fileWith finds the kubeconfig file that defines a context. $KUBECONFIG may list
// several files; the first one that has the context is the one kubectl uses.
func (m *Manager) fileWith(name string) (string, error) {
	for _, f := range clientcmd.NewDefaultClientConfigLoadingRules().GetLoadingPrecedence() {
		cfg, err := clientcmd.LoadFromFile(f)
		if err != nil {
			continue
		}
		if _, ok := cfg.Contexts[name]; ok {
			return f, nil
		}
	}
	return "", fmt.Errorf("no kubeconfig file defines %s", name)
}

func copyFile(src, dst string) (string, error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		return "", err
	}
	return dst, nil
}
