package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Kind is a resource type the UIs can list: a built-in or any CRD the server serves.
type Kind struct {
	Name       string   `json:"name"`  // plural resource name, e.g. "deployments"
	Kind       string   `json:"kind"`  // e.g. "Deployment"
	Group      string   `json:"group"` // "" for core
	Version    string   `json:"version"`
	Namespaced bool     `json:"namespaced"`
	ShortNames []string `json:"shortNames"`
	Verbs      []string `json:"verbs"`
}

func (k Kind) GVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: k.Group, Version: k.Version, Resource: k.Name}
}

// ID is how a kind is named on the command line and in the palette: "pods",
// "deployments.apps", "applications.argoproj.io".
func (k Kind) ID() string {
	if k.Group == "" {
		return k.Name
	}
	return k.Name + "." + k.Group
}

// Kinds lists every listable resource type the server serves, preferred versions only.
func (c *Cluster) Kinds() ([]Kind, error) {
	lists, err := c.Discovery.ServerPreferredResources()
	// A partial failure (one aggregated API down, typically metrics) still returns the
	// rest, and a browser missing one group is better than a browser with none.
	if err != nil && len(lists) == 0 {
		return nil, err
	}
	var out []Kind
	for _, list := range lists {
		gv, perr := schema.ParseGroupVersion(list.GroupVersion)
		if perr != nil {
			continue
		}
		for _, r := range list.APIResources {
			if strings.Contains(r.Name, "/") || !has(r.Verbs, "list") {
				continue // subresources (pods/log) and things that cannot be listed
			}
			out = append(out, Kind{
				Name: r.Name, Kind: r.Kind, Group: gv.Group, Version: gv.Version,
				Namespaced: r.Namespaced, ShortNames: r.ShortNames, Verbs: r.Verbs,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out, nil
}

// ResolveKind turns what a person types -- "po", "deploy", "pods", "Deployment",
// "applications.argoproj.io", "app" -- into a Kind.
func (c *Cluster) ResolveKind(name string) (Kind, error) {
	kinds, err := c.Kinds()
	if err != nil {
		return Kind{}, err
	}
	n := strings.ToLower(strings.TrimSpace(name))
	// Exact matches beat partial ones, and core beats a CRD of the same name, the way
	// kubectl resolves "events" to the core type rather than events.k8s.io.
	var best *Kind
	score := 0
	for i := range kinds {
		k := &kinds[i]
		s := 0
		switch {
		case n == k.ID():
			s = 100
		case n == k.Name || n == strings.ToLower(k.Kind) || has(k.ShortNames, n):
			s = 80
		case strings.TrimSuffix(n, "s") == strings.TrimSuffix(k.Name, "s"):
			s = 60
		}
		if s > 0 && k.Group == "" {
			s += 5
		}
		if s > score {
			best, score = k, s
		}
	}
	if best == nil {
		return Kind{}, fmt.Errorf("unknown resource type %q", name)
	}
	return *best, nil
}

// Row is one object as a table row: the generic columns plus whatever the kind adds.
type Row struct {
	Namespace string            `json:"namespace"`
	Name      string            `json:"name"`
	Age       string            `json:"age"`
	Created   time.Time         `json:"created"`
	Status    string            `json:"status"`
	Health    Health            `json:"health"`
	Cells     map[string]string `json:"cells"`
	Labels    map[string]string `json:"labels"`
}

// Health is the colour a row gets. Decided here so both UIs agree on what "bad" is.
type Health string

const (
	Healthy Health = "ok"
	Warning Health = "warn"
	Failing Health = "bad"
	Neutral Health = ""
)

// List returns every object of a kind, in one namespace or all ("" = all).
func (c *Cluster) List(ctx context.Context, k Kind, namespace string) ([]Row, []string, error) {
	ri := c.Dynamic.Resource(k.GVR())
	// Pods also get live CPU / memory, read alongside the list so it adds no wait.
	var usage map[string]Usage
	usageDone := make(chan struct{})
	if k.ID() == "pods" {
		go func() { defer close(usageDone); usage, _ = c.PodUsage(ctx, namespace) }()
	} else {
		close(usageDone)
	}
	var list *unstructured.UnstructuredList
	var err error
	if k.Namespaced && namespace != "" {
		list, err = ri.Namespace(namespace).List(ctx, metav1.ListOptions{})
	} else {
		list, err = ri.List(ctx, metav1.ListOptions{})
	}
	if err != nil {
		<-usageDone
		return nil, nil, err
	}
	cols := columnsFor(k)
	rows := make([]Row, 0, len(list.Items))
	for i := range list.Items {
		rows = append(rows, toRow(k, &list.Items[i]))
	}
	<-usageDone
	if k.ID() == "pods" {
		objs := make(map[string]map[string]any, len(list.Items))
		for i := range list.Items {
			objs[list.Items[i].GetNamespace()+"/"+list.Items[i].GetName()] = list.Items[i].Object
		}
		addUsage(rows, objs, usage)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Namespace != rows[j].Namespace {
			return rows[i].Namespace < rows[j].Namespace
		}
		return rows[i].Name < rows[j].Name
	})
	return rows, cols, nil
}

// Get returns one object, unstructured.
func (c *Cluster) Get(ctx context.Context, k Kind, namespace, name string) (*unstructured.Unstructured, error) {
	ri := c.Dynamic.Resource(k.GVR())
	if k.Namespaced {
		return ri.Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	}
	return ri.Get(ctx, name, metav1.GetOptions{})
}

// Namespaces lists namespace names, for the namespace picker.
func (c *Cluster) Namespaces(ctx context.Context) ([]string, error) {
	list, err := c.Typed.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(list.Items))
	for _, ns := range list.Items {
		out = append(out, ns.Name)
	}
	sort.Strings(out)
	return out, nil
}

// MappingFor resolves a group/kind to its resource, for objects read from YAML.
func (c *Cluster) MappingFor(gvk schema.GroupVersionKind) (*meta.RESTMapping, error) {
	m, err := c.Mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		c.Mapper.Reset() // a CRD installed since the cache was filled
		m, err = c.Mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	}
	return m, err
}

func has(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// HumanAge is kubectl's age format: 45s, 12m, 5h, 3d, 2y.
func HumanAge(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dy", int(d.Hours()/24/365))
	}
}

// PodsOf lists the pods a workload owns, through its label selector -- what "logs of
// all pods" of a Deployment, StatefulSet, DaemonSet, ReplicaSet or Job needs.
func (c *Cluster) PodsOf(ctx context.Context, k Kind, namespace, name string) ([]Row, error) {
	u, err := c.Get(ctx, k, namespace, name)
	if err != nil {
		return nil, err
	}
	sel, found, _ := unstructured.NestedFieldNoCopy(u.Object, "spec", "selector")
	if !found || sel == nil {
		return nil, fmt.Errorf("%s/%s has no pod selector", k.Kind, name)
	}
	var ls metav1.LabelSelector
	raw, _ := json.Marshal(sel)
	if err := json.Unmarshal(raw, &ls); err != nil {
		return nil, fmt.Errorf("reading the selector of %s: %w", name, err)
	}
	s, err := metav1.LabelSelectorAsSelector(&ls)
	if err != nil {
		return nil, err
	}
	pods, err := c.Dynamic.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).
		Namespace(namespace).List(ctx, metav1.ListOptions{LabelSelector: s.String()})
	if err != nil {
		return nil, err
	}
	podKind := Kind{Name: "pods", Version: "v1", Namespaced: true}
	out := make([]Row, 0, len(pods.Items))
	for i := range pods.Items {
		out = append(out, toRow(podKind, &pods.Items[i]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Probe is what the start screen shows for a cluster before you open it.
type Probe struct {
	Context string `json:"context"`
	OK      bool   `json:"ok"`
	Version string `json:"version,omitempty"`
	Nodes   int    `json:"nodes"`
	Ms      int64  `json:"ms"`
	Error   string `json:"error,omitempty"`
}

// ProbeCluster checks one cluster quickly: server version and node count. A short
// timeout, so an unreachable private endpoint shows as unreachable instead of hanging.
func (m *Manager) ProbeCluster(ctx context.Context, name string) Probe {
	start := time.Now()
	p := Probe{Context: name}
	defer func() { p.Ms = time.Since(start).Milliseconds() }()
	c, err := m.Cluster(name)
	if err != nil {
		p.Error = err.Error()
		return p
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	v, err := c.Typed.Discovery().RESTClient().Get().AbsPath("/version").Do(ctx).Raw()
	if err != nil {
		p.Error = err.Error()
		return p
	}
	var info struct{ GitVersion string }
	_ = json.Unmarshal(v, &info)
	p.Version = info.GitVersion
	if nodes, err := c.Typed.CoreV1().Nodes().List(ctx, metav1.ListOptions{}); err == nil {
		p.Nodes = len(nodes.Items)
	}
	p.OK = true
	return p
}

// KindRows is one kind's share of a namespace overview.
type KindRows struct {
	Kind    Kind     `json:"kind"`
	Columns []string `json:"columns"`
	Rows    []Row    `json:"rows"`
	Error   string   `json:"error,omitempty"`
}

// noisy kinds are left out of the namespace overview unless asked for: they are
// many, change constantly and say nothing about what runs there.
var noisy = map[string]bool{
	"events": true, "events.k8s.io": true, "leases.coordination.k8s.io": true,
	"endpointslices.discovery.k8s.io": true, "endpoints": true,
	"controllerrevisions.apps": true, "localsubjectaccessreviews.authorization.k8s.io": true,
	"policyreports.wgpolicyk8s.io": true, "ephemeralreports.reports.kyverno.io": true,
}

// Everything is `kubectl get all -n ns`, done properly: every namespaced kind the
// cluster serves and you may list -- not just the eight `all` covers -- fetched in
// parallel. Kinds with nothing in the namespace are left out; kinds you are not
// allowed to list come back with their error, so a missing kind is visible.
func (c *Cluster) Everything(ctx context.Context, namespace string, includeNoisy bool) ([]KindRows, error) {
	kinds, err := c.Kinds()
	if err != nil {
		return nil, err
	}
	var want []Kind
	for _, k := range kinds {
		if !k.Namespaced || (!includeNoisy && noisy[k.ID()]) {
			continue
		}
		want = append(want, k)
	}
	out := make([]KindRows, len(want))
	sem := make(chan struct{}, 16)
	var wg sync.WaitGroup
	for i, k := range want {
		wg.Add(1)
		go func(i int, k Kind) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			rows, cols, err := c.List(cctx, k, namespace)
			out[i] = KindRows{Kind: k, Columns: cols, Rows: rows}
			if err != nil {
				out[i].Error = err.Error()
			}
		}(i, k)
	}
	wg.Wait()
	res := out[:0]
	for _, r := range out {
		if len(r.Rows) > 0 || (r.Error != "" && !strings.Contains(r.Error, "could not find the requested resource")) {
			res = append(res, r)
		}
	}
	// Workloads first, then the rest by name: the order people read a namespace in.
	rank := map[string]int{"deployments.apps": 0, "statefulsets.apps": 1, "daemonsets.apps": 2, "replicasets.apps": 3,
		"pods": 4, "jobs.batch": 5, "cronjobs.batch": 6, "services": 7, "ingresses.networking.k8s.io": 8,
		"configmaps": 9, "secrets": 10, "persistentvolumeclaims": 11}
	sort.SliceStable(res, func(i, j int) bool {
		ri, iok := rank[res[i].Kind.ID()]
		rj, jok := rank[res[j].Kind.ID()]
		switch {
		case iok && jok:
			return ri < rj
		case iok != jok:
			return iok
		}
		return res[i].Kind.ID() < res[j].Kind.ID()
	})
	return res, nil
}
