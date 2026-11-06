package kube

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// The resource map of a namespace: how traffic and configuration flow through it.
//
//	Ingress ──▶ Service ──▶ Workload (Deployment, StatefulSet, DaemonSet, CronJob,
//	                        Job, bare Pod, or an operator's own kind) ──▶ ConfigMap /
//	                        Secret / PersistentVolumeClaim
//
// Pods are folded into their workload as coloured dots; ReplicaSets and a CronJob's
// Jobs are folded the same way, since nobody reasons about them separately. HPAs,
// KEDA ScaledObjects and PDBs become badges on the workload they act on. Edges are
// found the way Kubernetes finds them: a Service's selector against the workload's
// pod-template labels, an Ingress backend's service name, a pod spec's volumes,
// envFrom and env references.

// Graph columns, left to right.
const (
	ColIngress = iota
	ColService
	ColWorkload
	ColConfig
)

type GraphPod struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Health Health `json:"health"`
}

type GraphBadge struct {
	Text   string `json:"text"`
	Health Health `json:"health"`
	Title  string `json:"title,omitempty"`
}

type GraphNode struct {
	ID      string       `json:"id"`
	Kind    string       `json:"kind"`   // display kind, e.g. "Deployment"
	KindID  string       `json:"kindId"` // resource id for opening it, e.g. "deployments.apps"; "" if not openable
	Name    string       `json:"name"`
	Col     int          `json:"col"`
	Status  string       `json:"status"`
	Health  Health       `json:"health"`
	Info    string       `json:"info,omitempty"`
	Pods    []GraphPod   `json:"pods,omitempty"`
	PodsAll int          `json:"podsAll,omitempty"` // total pods when Pods is capped
	Badges  []GraphBadge `json:"badges,omitempty"`
	Missing bool         `json:"missing,omitempty"` // referenced but does not exist
}

type GraphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type Graph struct {
	Namespace string      `json:"namespace"`
	Nodes     []GraphNode `json:"nodes"`
	Edges     []GraphEdge `json:"edges"`
	Warnings  []string    `json:"warnings,omitempty"`
}

const maxPodDots = 60

type graphSrc struct {
	key, group, version, resource string
	optional                      bool // absent API (KEDA) is not a warning
}

var graphSources = []graphSrc{
	{"ingresses", "networking.k8s.io", "v1", "ingresses", false},
	{"services", "", "v1", "services", false},
	{"deployments", "apps", "v1", "deployments", false},
	{"statefulsets", "apps", "v1", "statefulsets", false},
	{"daemonsets", "apps", "v1", "daemonsets", false},
	{"replicasets", "apps", "v1", "replicasets", false},
	{"jobs", "batch", "v1", "jobs", false},
	{"cronjobs", "batch", "v1", "cronjobs", false},
	{"pods", "", "v1", "pods", false},
	{"configmaps", "", "v1", "configmaps", false},
	{"secrets", "", "v1", "secrets", false},
	{"pvcs", "", "v1", "persistentvolumeclaims", false},
	{"hpas", "autoscaling", "v2", "horizontalpodautoscalers", false},
	{"pdbs", "policy", "v1", "poddisruptionbudgets", false},
	{"scaledobjects", "keda.sh", "v1alpha1", "scaledobjects", true},
}

// kindFor gives the Kind used for health rules and for opening the object.
var kindFor = map[string]Kind{
	"ingresses":    {Name: "ingresses", Group: "networking.k8s.io", Version: "v1", Kind: "Ingress", Namespaced: true},
	"services":     {Name: "services", Version: "v1", Kind: "Service", Namespaced: true},
	"deployments":  {Name: "deployments", Group: "apps", Version: "v1", Kind: "Deployment", Namespaced: true},
	"statefulsets": {Name: "statefulsets", Group: "apps", Version: "v1", Kind: "StatefulSet", Namespaced: true},
	"daemonsets":   {Name: "daemonsets", Group: "apps", Version: "v1", Kind: "DaemonSet", Namespaced: true},
	"replicasets":  {Name: "replicasets", Group: "apps", Version: "v1", Kind: "ReplicaSet", Namespaced: true},
	"jobs":         {Name: "jobs", Group: "batch", Version: "v1", Kind: "Job", Namespaced: true},
	"cronjobs":     {Name: "cronjobs", Group: "batch", Version: "v1", Kind: "CronJob", Namespaced: true},
	"pods":         {Name: "pods", Version: "v1", Kind: "Pod", Namespaced: true},
	"configmaps":   {Name: "configmaps", Version: "v1", Kind: "ConfigMap", Namespaced: true},
	"secrets":      {Name: "secrets", Version: "v1", Kind: "Secret", Namespaced: true},
	"pvcs":         {Name: "persistentvolumeclaims", Version: "v1", Kind: "PersistentVolumeClaim", Namespaced: true},
}

// Graph builds the resource map of one namespace.
func (c *Cluster) Graph(ctx context.Context, namespace string) (*Graph, error) {
	if namespace == "" {
		return nil, fmt.Errorf("choose a namespace")
	}
	items := map[string][]unstructured.Unstructured{}
	g := &Graph{Namespace: namespace}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, s := range graphSources {
		wg.Add(1)
		go func(s graphSrc) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			gvr := schema.GroupVersionResource{Group: s.group, Version: s.version, Resource: s.resource}
			list, err := c.Dynamic.Resource(gvr).Namespace(namespace).List(cctx, metav1.ListOptions{})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if !s.optional {
					g.Warnings = append(g.Warnings, fmt.Sprintf("%s: %s", s.resource, firstLine(err.Error())))
				}
				return
			}
			items[s.key] = list.Items
		}(s)
	}
	wg.Wait()
	sort.Strings(g.Warnings)

	nodes := map[string]*GraphNode{}
	add := func(n *GraphNode) *GraphNode {
		if old, ok := nodes[n.ID]; ok {
			return old
		}
		nodes[n.ID] = n
		return n
	}
	edges := map[GraphEdge]bool{}
	link := func(from, to string) {
		if from != "" && to != "" && from != to {
			edges[GraphEdge{From: from, To: to}] = true
		}
	}
	nodeFor := func(key string, u *unstructured.Unstructured, col int) *GraphNode {
		k := kindFor[key]
		r := toRow(k, u)
		status := r.Status
		if status == "-" {
			status = ""
		}
		return &GraphNode{ID: k.Kind + "/" + u.GetName(), Kind: k.Kind, KindID: k.ID(), Name: u.GetName(),
			Col: col, Status: status, Health: r.Health}
	}

	// ---- workloads, and which workload each pod belongs to
	rsOwner := map[string]string{}  // ReplicaSet -> Deployment
	jobOwner := map[string]string{} // Job -> CronJob
	for i := range items["replicasets"] {
		if o := controller(&items["replicasets"][i]); o != nil && o.Kind == "Deployment" {
			rsOwner[items["replicasets"][i].GetName()] = o.Name
		}
	}
	for i := range items["jobs"] {
		if o := controller(&items["jobs"][i]); o != nil && o.Kind == "CronJob" {
			jobOwner[items["jobs"][i].GetName()] = o.Name
		}
	}
	templates := map[string]map[string]any{}     // workload id -> pod template spec
	tmplLabels := map[string]map[string]string{} // workload id -> pod template labels
	addWorkload := func(key string, u *unstructured.Unstructured, tmplPath ...string) {
		n := add(nodeFor(key, u, ColWorkload))
		n.Info = workloadInfo(key, u, n)
		if spec, ok := lookup(u.Object, append(tmplPath, "spec")...).(map[string]any); ok {
			templates[n.ID] = spec
		}
		if lb, ok := lookup(u.Object, append(tmplPath, "metadata", "labels")...).(map[string]any); ok {
			tmplLabels[n.ID] = toStrMap(lb)
		}
	}
	for _, key := range []string{"deployments", "statefulsets", "daemonsets"} {
		for i := range items[key] {
			addWorkload(key, &items[key][i], "spec", "template")
		}
	}
	for i := range items["cronjobs"] {
		addWorkload("cronjobs", &items["cronjobs"][i], "spec", "jobTemplate", "spec", "template")
	}
	for i := range items["jobs"] {
		if jobOwner[items["jobs"][i].GetName()] == "" {
			addWorkload("jobs", &items["jobs"][i], "spec", "template")
		}
	}
	for i := range items["replicasets"] {
		u := &items["replicasets"][i]
		if rsOwner[u.GetName()] == "" && (num(u.Object, "spec", "replicas") > 0 || controller(u) == nil) {
			addWorkload("replicasets", u, "spec", "template")
		}
	}
	podKind := kindFor["pods"]
	for i := range items["pods"] {
		p := &items["pods"][i]
		pr := toRow(podKind, p)
		wid := ""
		switch o := controller(p); {
		case o == nil:
			n := add(nodeFor("pods", p, ColWorkload))
			n.Info = str(p.Object, "spec", "nodeName")
			templates[n.ID] = mapOf(p.Object, "spec")
			tmplLabels[n.ID] = p.GetLabels()
			continue
		case o.Kind == "ReplicaSet" && rsOwner[o.Name] != "":
			wid = "Deployment/" + rsOwner[o.Name]
		case o.Kind == "Job" && jobOwner[o.Name] != "":
			wid = "CronJob/" + jobOwner[o.Name]
		default:
			wid = o.Kind + "/" + o.Name
		}
		n, ok := nodes[wid]
		if !ok {
			// An operator's own kind (an Aerospike cluster, a Spark app…): show it as
			// a workload, with its health taken from its pods.
			n = add(&GraphNode{ID: wid, Kind: o0(controller(p)).Kind, KindID: "", Name: o0(controller(p)).Name, Col: ColWorkload})
			templates[wid] = mapOf(p.Object, "spec")
			tmplLabels[wid] = p.GetLabels()
		}
		n.PodsAll++
		if len(n.Pods) < maxPodDots {
			n.Pods = append(n.Pods, GraphPod{Name: p.GetName(), Status: pr.Status, Health: pr.Health})
		}
		if _, ok := tmplLabels[wid]; !ok {
			tmplLabels[wid] = p.GetLabels()
		}
	}
	for _, n := range nodes {
		if n.Col == ColWorkload && n.KindID == "" {
			n.Health, n.Status = worstPod(n.Pods)
			n.Info = fmt.Sprintf("%d pod%s", n.PodsAll, plural(n.PodsAll))
		}
		sort.Slice(n.Pods, func(i, j int) bool { return n.Pods[i].Name < n.Pods[j].Name })
	}

	// ---- services -> workloads (selector ⊆ pod-template labels)
	svcByName := map[string]bool{}
	for i := range items["services"] {
		s := &items["services"][i]
		svcByName[s.GetName()] = true
		n := add(nodeFor("services", s, ColService))
		n.Info = serviceInfo(s)
		sel := toStrMap(mapOf(s.Object, "spec", "selector"))
		matched := 0
		if len(sel) > 0 {
			for wid, labels := range tmplLabels {
				if subset(sel, labels) {
					link(n.ID, wid)
					matched++
				}
			}
		}
		switch {
		case str(s.Object, "spec", "type") == "ExternalName":
			n.Health, n.Status = Neutral, "ExternalName"
		case len(sel) == 0:
			n.Health, n.Status = Neutral, "No selector"
		case matched == 0:
			n.Health, n.Status = Warning, "Selects nothing"
		default:
			n.Health, n.Status = Healthy, ""
		}
	}

	// ---- ingresses -> services
	for i := range items["ingresses"] {
		in := &items["ingresses"][i]
		n := add(nodeFor("ingresses", in, ColIngress))
		n.Info = ingressHosts(in.Object)
		missing := 0
		for _, svc := range ingressBackends(in.Object) {
			id := "Service/" + svc
			if !svcByName[svc] {
				add(&GraphNode{ID: id, Kind: "Service", Name: svc, Col: ColService, Missing: true, Health: Failing, Status: "Missing"})
				missing++
			}
			link(n.ID, id)
		}
		addr := lbAddress(in.Object)
		switch {
		case missing > 0:
			n.Health, n.Status = Failing, "Backend missing"
		case addr == "-":
			n.Health, n.Status = Warning, "No address"
		default:
			n.Health, n.Status = Healthy, ""
		}
	}

	// ---- workloads -> config and storage
	have := map[string]map[string]*unstructured.Unstructured{}
	for _, key := range []string{"configmaps", "secrets", "pvcs"} {
		have[key] = map[string]*unstructured.Unstructured{}
		for i := range items[key] {
			have[key][items[key][i].GetName()] = &items[key][i]
		}
	}
	ref := func(wid, key, name string, opt bool) {
		if name == "" {
			return
		}
		k := kindFor[key]
		id := k.Kind + "/" + name
		if u, ok := have[key][name]; ok {
			n := add(nodeFor(key, u, ColConfig))
			n.Info = configInfo(key, u)
			if key != "pvcs" {
				n.Status, n.Health = "", Neutral
			}
		} else if _, listed := items[key]; listed {
			n := add(&GraphNode{ID: id, Kind: k.Kind, Name: name, Col: ColConfig, Missing: true, Health: Failing, Status: "Missing"})
			if opt && n.Health == Failing && n.Status == "Missing" {
				n.Health, n.Status, n.Info = Neutral, "Optional, absent", "referenced with optional: true"
			} else if !opt {
				n.Health, n.Status, n.Info = Failing, "Missing", ""
			}
		} else {
			return // that kind couldn't be listed (no permission); don't claim it's missing
		}
		link(wid, id)
	}
	for wid, spec := range templates {
		for _, r := range podSpecRefs(spec) {
			ref(wid, r[0], r[1], r[2] == "optional")
		}
	}
	for i := range items["statefulsets"] {
		sts := &items["statefulsets"][i]
		vcts, _ := lookup(sts.Object, "spec", "volumeClaimTemplates").([]any)
		for _, v := range vcts {
			t := str(v.(map[string]any), "metadata", "name")
			prefix := t + "-" + sts.GetName() + "-"
			for name := range have["pvcs"] {
				if strings.HasPrefix(name, prefix) {
					ref("StatefulSet/"+sts.GetName(), "pvcs", name, false)
				}
			}
		}
	}

	// ---- badges: HPA, KEDA, PDB
	badge := func(wid string, b GraphBadge) {
		if n, ok := nodes[wid]; ok {
			n.Badges = append(n.Badges, b)
		}
	}
	for i := range items["hpas"] {
		h := items["hpas"][i].Object
		wid := str(h, "spec", "scaleTargetRef", "kind") + "/" + str(h, "spec", "scaleTargetRef", "name")
		badge(wid, GraphBadge{Text: fmt.Sprintf("HPA %s–%s · %d", orDash(str(h, "spec", "minReplicas")), str(h, "spec", "maxReplicas"), num(h, "status", "currentReplicas")),
			Title: "HorizontalPodAutoscaler " + items["hpas"][i].GetName()})
	}
	for i := range items["scaledobjects"] {
		s := &items["scaledobjects"][i]
		kind := str(s.Object, "spec", "scaleTargetRef", "kind")
		if kind == "" {
			kind = "Deployment"
		}
		_, h := readyCondition(s.Object)
		badge(kind+"/"+str(s.Object, "spec", "scaleTargetRef", "name"), GraphBadge{
			Text:   fmt.Sprintf("KEDA %s–%s", orDash(str(s.Object, "spec", "minReplicaCount")), orDash(str(s.Object, "spec", "maxReplicaCount"))),
			Health: h, Title: "ScaledObject " + s.GetName()})
	}
	for i := range items["pdbs"] {
		p := items["pdbs"][i].Object
		sel := toStrMap(mapOf(p, "spec", "selector", "matchLabels"))
		if len(sel) == 0 {
			continue
		}
		blocks := num(p, "status", "disruptionsAllowed") == 0 && num(p, "status", "expectedPods") > 0
		b := GraphBadge{Text: "PDB", Title: "PodDisruptionBudget " + items["pdbs"][i].GetName()}
		if blocks {
			b.Text, b.Health, b.Title = "PDB blocks drains", Warning, b.Title+": allows no disruption, so nodes can't be drained"
		}
		for wid, labels := range tmplLabels {
			if subset(sel, labels) {
				badge(wid, b)
			}
		}
	}

	for _, n := range nodes {
		g.Nodes = append(g.Nodes, *n)
	}
	sort.Slice(g.Nodes, func(i, j int) bool {
		if g.Nodes[i].Col != g.Nodes[j].Col {
			return g.Nodes[i].Col < g.Nodes[j].Col
		}
		return g.Nodes[i].ID < g.Nodes[j].ID
	})
	for e := range edges {
		g.Edges = append(g.Edges, e)
	}
	sort.Slice(g.Edges, func(i, j int) bool {
		if g.Edges[i].From != g.Edges[j].From {
			return g.Edges[i].From < g.Edges[j].From
		}
		return g.Edges[i].To < g.Edges[j].To
	})
	return g, nil
}

type ownerRef struct{ Kind, Name string }

func controller(u *unstructured.Unstructured) *ownerRef {
	refs := u.GetOwnerReferences()
	for _, r := range refs {
		if r.Controller != nil && *r.Controller {
			return &ownerRef{r.Kind, r.Name}
		}
	}
	if len(refs) > 0 {
		return &ownerRef{refs[0].Kind, refs[0].Name}
	}
	return nil
}

func o0(o *ownerRef) ownerRef {
	if o == nil {
		return ownerRef{}
	}
	return *o
}

func mapOf(o map[string]any, path ...string) map[string]any {
	m, _ := lookup(o, path...).(map[string]any)
	return m
}

func toStrMap(m map[string]any) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = fmt.Sprint(v)
	}
	return out
}

// subset reports whether every key=value of sel is in labels.
func subset(sel, labels map[string]string) bool {
	if len(sel) == 0 {
		return false
	}
	for k, v := range sel {
		if labels[k] != v {
			return false
		}
	}
	return true
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func worstPod(pods []GraphPod) (Health, string) {
	bad, warn := 0, 0
	for _, p := range pods {
		switch p.Health {
		case Failing:
			bad++
		case Warning:
			warn++
		}
	}
	switch {
	case len(pods) == 0:
		return Neutral, ""
	case bad > 0:
		return Failing, fmt.Sprintf("%d failing", bad)
	case warn > 0:
		return Warning, fmt.Sprintf("%d not ready", warn)
	}
	return Healthy, "Running"
}

func workloadInfo(key string, u *unstructured.Unstructured, n *GraphNode) string {
	o := u.Object
	switch key {
	case "deployments", "statefulsets", "replicasets":
		return fmt.Sprintf("%d/%d ready · %s", num(o, "status", "readyReplicas"), num(o, "spec", "replicas"),
			firstImage(o, "spec", "template", "spec", "containers"))
	case "daemonsets":
		return fmt.Sprintf("%d/%d ready · %s", num(o, "status", "numberReady"), num(o, "status", "desiredNumberScheduled"),
			firstImage(o, "spec", "template", "spec", "containers"))
	case "cronjobs":
		s := str(o, "spec", "schedule")
		if boolean(o, "spec", "suspend") {
			s += " · suspended"
		}
		return s
	case "jobs":
		return fmt.Sprintf("%d succeeded · %d failed", num(o, "status", "succeeded"), num(o, "status", "failed"))
	}
	return ""
}

func serviceInfo(s *unstructured.Unstructured) string {
	o := s.Object
	t := str(o, "spec", "type")
	if t == "ExternalName" {
		return "→ " + str(o, "spec", "externalName")
	}
	info := t + " " + servicePorts(o)
	if t == "LoadBalancer" {
		if a := lbAddress(o); a != "-" {
			info += " · " + a
		}
	}
	return info
}

func ingressBackends(o map[string]any) []string {
	seen := map[string]bool{}
	var out []string
	addName := func(n string) {
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	addName(str(o, "spec", "defaultBackend", "service", "name"))
	rules, _ := lookup(o, "spec", "rules").([]any)
	for _, r := range rules {
		paths, _ := lookup(r.(map[string]any), "http", "paths").([]any)
		for _, p := range paths {
			addName(str(p.(map[string]any), "backend", "service", "name"))
		}
	}
	sort.Strings(out)
	return out
}

func configInfo(key string, u *unstructured.Unstructured) string {
	o := u.Object
	switch key {
	case "configmaps":
		d, _ := o["data"].(map[string]any)
		b, _ := o["binaryData"].(map[string]any)
		return fmt.Sprintf("%d key%s", len(d)+len(b), plural(len(d)+len(b)))
	case "secrets":
		d, _ := o["data"].(map[string]any)
		t := str(o, "type")
		t = strings.TrimPrefix(t, "kubernetes.io/")
		return fmt.Sprintf("%s · %d key%s", t, len(d), plural(len(d)))
	case "pvcs":
		return strings.TrimSpace(str(o, "status", "capacity", "storage") + " " + str(o, "spec", "storageClassName"))
	}
	return ""
}

// podSpecRefs lists the ConfigMaps, Secrets and PVCs a pod spec uses, as
// [kind key, name] pairs.
func podSpecRefs(spec map[string]any) [][3]string {
	var out [][3]string
	idx := map[[2]string]int{}
	// optional ("" or "optional"): a reference marked optional:true may be absent --
	// Argo CD does this for its TLS secrets -- and is shown as such, not as missing.
	// A name referenced both ways counts as required.
	addRef := func(key, name string, opt ...bool) {
		if name == "" {
			return
		}
		o := ""
		if len(opt) > 0 && opt[0] {
			o = "optional"
		}
		k := [2]string{key, name}
		if i, ok := idx[k]; ok {
			if o == "" {
				out[i][2] = ""
			}
			return
		}
		idx[k] = len(out)
		out = append(out, [3]string{key, name, o})
	}
	optional := func(m map[string]any, path ...string) bool { return boolean(m, append(path, "optional")...) }
	vols, _ := spec["volumes"].([]any)
	for _, v := range vols {
		m, _ := v.(map[string]any)
		addRef("configmaps", str(m, "configMap", "name"), optional(m, "configMap"))
		addRef("secrets", str(m, "secret", "secretName"), optional(m, "secret"))
		addRef("pvcs", str(m, "persistentVolumeClaim", "claimName"))
		srcs, _ := lookup(m, "projected", "sources").([]any)
		for _, s := range srcs {
			sm, _ := s.(map[string]any)
			addRef("configmaps", str(sm, "configMap", "name"), optional(sm, "configMap"))
			addRef("secrets", str(sm, "secret", "name"), optional(sm, "secret"))
		}
	}
	for _, field := range []string{"initContainers", "containers"} {
		cs, _ := spec[field].([]any)
		for _, c := range cs {
			cm, _ := c.(map[string]any)
			ef, _ := cm["envFrom"].([]any)
			for _, e := range ef {
				em, _ := e.(map[string]any)
				addRef("configmaps", str(em, "configMapRef", "name"), optional(em, "configMapRef"))
				addRef("secrets", str(em, "secretRef", "name"), optional(em, "secretRef"))
			}
			env, _ := cm["env"].([]any)
			for _, e := range env {
				em, _ := e.(map[string]any)
				addRef("configmaps", str(em, "valueFrom", "configMapKeyRef", "name"), optional(em, "valueFrom", "configMapKeyRef"))
				addRef("secrets", str(em, "valueFrom", "secretKeyRef", "name"), optional(em, "valueFrom", "secretKeyRef"))
			}
		}
	}
	ips, _ := spec["imagePullSecrets"].([]any)
	for _, s := range ips {
		addRef("secrets", str(s.(map[string]any), "name"))
	}
	return out
}
