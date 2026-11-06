package kube

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ClusterMap is the whole cluster at the level a person can take in at once: where
// capacity is (node pools, their nodes, how full they are) and what runs on it
// (namespaces, their workloads and pods, how healthy). Each namespace links to the
// pools its pods are scheduled on, with the pod count on the link. A per-resource
// graph of a 750-pod cluster would be unreadable; the namespace map (Graph) is one
// click away for the detail.
type ClusterMap struct {
	Pools      []MapPool      `json:"pools"`
	Namespaces []MapNamespace `json:"namespaces"`
	Links      []MapLink      `json:"links"`
	Ingresses  int            `json:"ingresses"`
	Nodes      int            `json:"nodes"`
	Pods       int            `json:"pods"`
	Warnings   []string       `json:"warnings,omitempty"`
}

type MapPool struct {
	Name     string    `json:"name"`
	Source   string    `json:"source"` // "karpenter", "eks", or "" for unlabelled nodes
	Nodes    []MapNode `json:"nodes"`
	CPUReq   int64     `json:"cpuReq"`   // millicores requested by pods on the pool
	CPUAlloc int64     `json:"cpuAlloc"` // millicores allocatable on the pool
	MemReq   int64     `json:"memReq"`   // bytes
	MemAlloc int64     `json:"memAlloc"`
	Pods     int       `json:"pods"`
	Health   Health    `json:"health"`
	Types    []string  `json:"types"` // instance types in the pool
}

type MapNode struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Zone   string `json:"zone"`
	Spot   bool   `json:"spot"`
	Pods   int    `json:"pods"`
	CPUPct int    `json:"cpuPct"` // requested / allocatable
	Health Health `json:"health"`
	Status string `json:"status"`
}

type MapNamespace struct {
	Name      string `json:"name"`
	Workloads int    `json:"workloads"`
	Pods      int    `json:"pods"`
	Failing   int    `json:"failing"`
	Warning   int    `json:"warning"`
	Services  int    `json:"services"`
	Ingresses int    `json:"ingresses"`
	Health    Health `json:"health"`
	System    bool   `json:"system"` // kube-system, karpenter… -- the platform, not the apps
	CPUReq    int64  `json:"cpuReq"`
	MemReq    int64  `json:"memReq"`
}

type MapLink struct {
	Namespace string `json:"namespace"`
	Pool      string `json:"pool"`
	Pods      int    `json:"pods"`
}

var systemNS = map[string]bool{"kube-system": true, "kube-public": true, "kube-node-lease": true, "karpenter": true,
	"cert-manager": true, "external-dns": true, "monitoring": true, "argocd": true, "crossplane-system": true,
	"infra": true, "keda": true, "kyverno": true, "amazon-cloudwatch": true, "aws-observability": true, "default": true}

// poolOf names the pool a node belongs to, preferring Karpenter, then EKS node groups.
func poolOf(labels map[string]string) (string, string) {
	if p := labels["karpenter.sh/nodepool"]; p != "" {
		return p, "karpenter"
	}
	if p := labels["eks.amazonaws.com/nodegroup"]; p != "" {
		return p, "eks"
	}
	if p := labels["alpha.eksctl.io/nodegroup-name"]; p != "" {
		return p, "eks"
	}
	if labels["eks.amazonaws.com/compute-type"] == "fargate" {
		return "fargate", "eks"
	}
	return "other nodes", ""
}

// ClusterMap builds the cluster overview from nodes, pods and a few counts.
func (c *Cluster) ClusterMap(ctx context.Context) (*ClusterMap, error) {
	cm := &ClusterMap{Pools: []MapPool{}, Namespaces: []MapNamespace{}, Links: []MapLink{}}
	var (
		nodes []corev1.Node
		pods  []corev1.Pod
		mu    sync.Mutex
		wg    sync.WaitGroup
		owned = map[string]map[string]bool{} // namespace -> workload ids
		svcs  = map[string]int{}
		ings  = map[string]int{}
	)
	warn := func(what string, err error) {
		mu.Lock()
		cm.Warnings = append(cm.Warnings, what+": "+firstLine(err.Error()))
		mu.Unlock()
	}
	run := func(f func(context.Context)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			f(cctx)
		}()
	}
	run(func(ctx context.Context) {
		l, err := c.Typed.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			warn("nodes", err)
			return
		}
		nodes = l.Items
	})
	run(func(ctx context.Context) {
		l, err := c.Typed.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
		if err != nil {
			warn("pods", err)
			return
		}
		pods = l.Items
	})
	for _, r := range []struct {
		res  schema.GroupVersionResource
		into map[string]int
	}{
		{schema.GroupVersionResource{Version: "v1", Resource: "services"}, svcs},
		{schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}, ings},
	} {
		r := r
		run(func(ctx context.Context) {
			l, err := c.Dynamic.Resource(r.res).List(ctx, metav1.ListOptions{})
			if err != nil {
				warn(r.res.Resource, err)
				return
			}
			mu.Lock()
			for _, it := range l.Items {
				r.into[it.GetNamespace()]++
			}
			mu.Unlock()
		})
	}
	wg.Wait()

	// ---- pools and nodes
	pools := map[string]*MapPool{}
	nodePool := map[string]string{}
	nodeIdx := map[string]*MapNode{}
	for i := range nodes {
		n := &nodes[i]
		name, src := poolOf(n.Labels)
		p := pools[name]
		if p == nil {
			p = &MapPool{Name: name, Source: src}
			pools[name] = p
		}
		ready := false
		for _, cond := range n.Status.Conditions {
			if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
				ready = true
			}
		}
		mn := MapNode{Name: n.Name, Type: n.Labels["node.kubernetes.io/instance-type"], Zone: n.Labels["topology.kubernetes.io/zone"],
			Spot:   n.Labels["karpenter.sh/capacity-type"] == "spot" || n.Labels["eks.amazonaws.com/capacityType"] == "SPOT",
			Health: Healthy, Status: "Ready"}
		switch {
		case !ready:
			mn.Health, mn.Status = Failing, "NotReady"
		case n.Spec.Unschedulable:
			mn.Health, mn.Status = Warning, "Cordoned"
		}
		p.CPUAlloc += n.Status.Allocatable.Cpu().MilliValue()
		p.MemAlloc += n.Status.Allocatable.Memory().Value()
		p.Nodes = append(p.Nodes, mn)
		nodePool[n.Name] = name
	}
	for _, p := range pools {
		sort.Slice(p.Nodes, func(i, j int) bool { return p.Nodes[i].Name < p.Nodes[j].Name })
		for i := range p.Nodes {
			nodeIdx[p.Nodes[i].Name] = &p.Nodes[i]
		}
	}
	nodeCPUReq := map[string]int64{}
	nodeAlloc := map[string]int64{}
	for i := range nodes {
		nodeAlloc[nodes[i].Name] = nodes[i].Status.Allocatable.Cpu().MilliValue()
	}

	// ---- namespaces, pods, links
	nss := map[string]*MapNamespace{}
	links := map[[2]string]int{}
	podKind := Kind{Name: "pods", Version: "v1", Namespaced: true}
	for i := range pods {
		p := &pods[i]
		ns := nss[p.Namespace]
		if ns == nil {
			ns = &MapNamespace{Name: p.Namespace, System: systemNS[p.Namespace] || strings.HasPrefix(p.Namespace, "kube-")}
			nss[p.Namespace] = ns
			owned[p.Namespace] = map[string]bool{}
		}
		done := p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed
		if !done {
			ns.Pods++
			cm.Pods++
		}
		if len(p.OwnerReferences) > 0 {
			o := p.OwnerReferences[0]
			key := o.Kind + "/" + o.Name
			if o.Kind == "ReplicaSet" {
				// Deployment pods: the ReplicaSet name is "<deployment>-<hash>".
				if i := strings.LastIndex(o.Name, "-"); i > 0 {
					key = "Deployment/" + o.Name[:i]
				}
			} else if o.Kind == "Job" {
				if i := strings.LastIndex(o.Name, "-"); i > 0 {
					key = "CronJob/" + o.Name[:i]
				}
			}
			owned[p.Namespace][key] = true
		} else {
			owned[p.Namespace]["Pod/"+p.Name] = true
		}
		if done {
			if p.Status.Phase == corev1.PodFailed {
				ns.Failing++
			}
			continue
		}
		var cpu, mem int64
		for _, ctr := range p.Spec.Containers {
			cpu += ctr.Resources.Requests.Cpu().MilliValue()
			mem += ctr.Resources.Requests.Memory().Value()
		}
		ns.CPUReq += cpu
		ns.MemReq += mem
		r := toRowPod(podKind, p)
		switch r {
		case Failing:
			ns.Failing++
		case Warning:
			ns.Warning++
		}
		if p.Spec.NodeName != "" {
			pool := nodePool[p.Spec.NodeName]
			if pool == "" {
				pool = "other nodes"
			}
			links[[2]string{p.Namespace, pool}]++
			if pp := pools[pool]; pp != nil {
				pp.Pods++
				pp.CPUReq += cpu
				pp.MemReq += mem
			}
			if n := nodeIdx[p.Spec.NodeName]; n != nil {
				n.Pods++
			}
			nodeCPUReq[p.Spec.NodeName] += cpu
		}
	}
	for name, n := range nodeIdx {
		if a := nodeAlloc[name]; a > 0 {
			n.CPUPct = int(nodeCPUReq[name] * 100 / a)
		}
	}
	for name, ns := range nss {
		ns.Workloads = len(owned[name])
		ns.Services = svcs[name]
		ns.Ingresses = ings[name]
		cm.Ingresses += ns.Ingresses
		switch {
		case ns.Failing > 0:
			ns.Health = Failing
		case ns.Warning > 0:
			ns.Health = Warning
		case ns.Pods > 0:
			ns.Health = Healthy
		}
		cm.Namespaces = append(cm.Namespaces, *ns)
	}
	sort.Slice(cm.Namespaces, func(i, j int) bool {
		a, b := cm.Namespaces[i], cm.Namespaces[j]
		if a.System != b.System {
			return !a.System
		}
		return a.Name < b.Name
	})
	for _, p := range pools {
		seen := map[string]bool{}
		bad := 0
		for _, n := range p.Nodes {
			if n.Type != "" && !seen[n.Type] {
				seen[n.Type] = true
				p.Types = append(p.Types, n.Type)
			}
			if n.Health != Healthy {
				bad++
			}
		}
		if p.Types == nil {
			p.Types = []string{} // [] not null, for the page
		}
		sort.Strings(p.Types)
		switch {
		case bad == len(p.Nodes) && bad > 0:
			p.Health = Failing
		case bad > 0:
			p.Health = Warning
		default:
			p.Health = Healthy
		}
		cm.Pools = append(cm.Pools, *p)
		cm.Nodes += len(p.Nodes)
	}
	sort.Slice(cm.Pools, func(i, j int) bool {
		if (cm.Pools[i].Name == "other nodes") != (cm.Pools[j].Name == "other nodes") {
			return cm.Pools[j].Name == "other nodes"
		}
		return len(cm.Pools[i].Nodes) > len(cm.Pools[j].Nodes) || (len(cm.Pools[i].Nodes) == len(cm.Pools[j].Nodes) && cm.Pools[i].Name < cm.Pools[j].Name)
	})
	for k, n := range links {
		cm.Links = append(cm.Links, MapLink{Namespace: k[0], Pool: k[1], Pods: n})
	}
	sort.Slice(cm.Links, func(i, j int) bool {
		return cm.Links[i].Namespace+cm.Links[i].Pool < cm.Links[j].Namespace+cm.Links[j].Pool
	})
	sort.Strings(cm.Warnings)
	if len(nodes) == 0 && len(pods) == 0 && len(cm.Warnings) > 0 {
		return nil, fmt.Errorf("could not read the cluster: %s", strings.Join(cm.Warnings, "; "))
	}
	return cm, nil
}

// toRowPod is the pod health rule from columns.go, for a typed Pod.
func toRowPod(k Kind, p *corev1.Pod) Health {
	ready, total := 0, len(p.Spec.Containers)
	reason := ""
	for _, s := range p.Status.ContainerStatuses {
		if s.Ready {
			ready++
		}
		if s.State.Waiting != nil && reason == "" {
			reason = s.State.Waiting.Reason
		}
	}
	switch {
	case p.DeletionTimestamp != nil:
		return Warning
	case reason == "CrashLoopBackOff" || reason == "ImagePullBackOff" || reason == "ErrImagePull" || reason == "CreateContainerConfigError":
		return Failing
	case p.Status.Phase == corev1.PodRunning && ready == total:
		return Healthy
	case p.Status.Phase == corev1.PodPending || p.Status.Phase == corev1.PodRunning:
		return Warning
	}
	return Neutral
}
