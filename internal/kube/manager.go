// Package kube is the cluster layer shared by the terminal and desktop front ends.
// Everything that talks to a Kubernetes API server lives here, so the two UIs differ
// only in how they draw, never in what they do to a cluster.
package kube

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// Cluster holds the clients for one kubeconfig context. They are built on first use
// and kept: building one runs the exec credential plugin (aws eks get-token), which
// is the slow part of talking to EKS.
type Cluster struct {
	Context   string
	Namespace string // the context's default namespace, "" if none
	Config    *rest.Config
	Typed     kubernetes.Interface
	Dynamic   dynamic.Interface
	Discovery discovery.CachedDiscoveryInterface
	Mapper    meta.ResettableRESTMapper

	pcMu sync.Mutex
	pc   map[string]printerCache // a CRD's own columns, by kind id; see printercols.go
}

// Manager knows every context in the kubeconfig and hands out a Cluster per context.
type Manager struct {
	loader clientcmd.ClientConfig
	raw    clientcmdapi.Config
	safe   *regexp.Regexp

	mu       sync.Mutex
	clusters map[string]*Cluster
	hidden   map[string]bool // contexts hidden from the lists; see hidden.go
}

// DefaultSafe matches the contexts where writes need no typed confirmation: only
// clusters running on this machine (kind, minikube, Docker/Rancher Desktop, k3d,
// OrbStack). Every real cluster is protected, dev ones included -- the user's choice:
// a dev cluster is still shared, and a name is a poor guide to what a cluster is.
// KV_SAFE=<regexp> exempts more.
const DefaultSafe = `(?i)^(kind-.+|minikube|docker-desktop|rancher-desktop|k3d-.+|orbstack|colima)$`

// NewManager reads the kubeconfig the same way kubectl does: $KUBECONFIG, else
// ~/.kube/config, merged.
func NewManager() (*Manager, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	loader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{})
	raw, err := loader.RawConfig()
	if err != nil {
		return nil, fmt.Errorf("reading kubeconfig: %w", err)
	}
	pattern := os.Getenv("KV_SAFE")
	if pattern == "" {
		pattern = DefaultSafe
	}
	safe, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("KV_SAFE is not a valid regexp: %w", err)
	}
	return &Manager{loader: loader, raw: raw, safe: safe, clusters: map[string]*Cluster{}, hidden: loadHidden()}, nil
}

// ContextInfo is one kubeconfig context as the UIs list it.
type ContextInfo struct {
	Name      string `json:"name"`
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
	Current   bool   `json:"current"`
	Protected bool   `json:"protected"`
	Hidden    bool   `json:"hidden"`
}

// Contexts lists the contexts K-Vision shows: every kubeconfig context except
// the hidden ones. The current context is always shown, hidden or not, so the app
// never opens on a cluster it does not list.
func (m *Manager) Contexts() []ContextInfo {
	var out []ContextInfo
	for _, c := range m.AllContexts() {
		if !c.Hidden || c.Current {
			out = append(out, c)
		}
	}
	return out
}

// AllContexts is every kubeconfig context, hidden ones included and marked.
func (m *Manager) AllContexts() []ContextInfo {
	m.mu.Lock()
	hidden := make(map[string]bool, len(m.hidden))
	for k, v := range m.hidden {
		hidden[k] = v
	}
	m.mu.Unlock()
	out := make([]ContextInfo, 0, len(m.raw.Contexts))
	for name, c := range m.raw.Contexts {
		out = append(out, ContextInfo{
			Name:      name,
			Cluster:   c.Cluster,
			Namespace: c.Namespace,
			Current:   name == m.raw.CurrentContext,
			Protected: m.Protected(name),
			Hidden:    hidden[name],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (m *Manager) CurrentContext() string { return m.raw.CurrentContext }

// Protected reports whether writes to this context need typed confirmation.
func (m *Manager) Protected(context string) bool {
	// Judged on the cluster's own name, not the ARN or alias around it: an EKS
	// context is "arn:aws:eks:eu-central-1:1234:cluster/payments-dev".
	name := context
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return !m.safe.MatchString(name)
}

// Cluster returns the clients for a context, building them on first use.
func (m *Manager) Cluster(context string) (*Cluster, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.clusters[context]; ok {
		return c, nil
	}
	kctx, ok := m.raw.Contexts[context]
	if !ok {
		return nil, fmt.Errorf("no context %q in kubeconfig", context)
	}
	cfg, err := clientcmd.NewNonInteractiveClientConfig(m.raw, context, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("context %s: %w", context, err)
	}
	// The defaults (5 qps) make a multi-cluster view crawl.
	cfg.QPS, cfg.Burst = 50, 100
	c, err := newCluster(context, kctx.Namespace, cfg)
	if err != nil {
		return nil, err
	}
	m.clusters[context] = c
	return c, nil
}

func newCluster(context, namespace string, cfg *rest.Config) (*Cluster, error) {
	typed, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	disco := memory.NewMemCacheClient(typed.Discovery())
	return &Cluster{
		Context:   context,
		Namespace: namespace,
		Config:    cfg,
		Typed:     typed,
		Dynamic:   dyn,
		Discovery: disco,
		Mapper:    restmapper.NewDeferredDiscoveryRESTMapper(disco),
	}, nil
}

// NewClusterForConfig wraps an existing rest.Config; used by tests and by anything
// that already has a config rather than a kubeconfig context.
func NewClusterForConfig(context string, cfg *rest.Config) (*Cluster, error) {
	return newCluster(context, "", cfg)
}
