package kube

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// HelmMap answers "which Helm release created what": every release with the
// objects in its current manifest, each checked against the cluster -- live status
// and health from the same per-kind rules as the lists, or Missing when Helm
// installed it but it is gone (deleted by hand, or the install half failed).

type HelmMapRelease struct {
	HelmRelease
	Objects []HelmObject `json:"objects"`
	Counts  struct {
		Total, Missing, Failing, Warning int
	} `json:"counts"`
}

type HelmObject struct {
	Kind      string `json:"kind"`
	KindID    string `json:"kindId"` // for opening it; "" when the cluster doesn't serve the kind
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Health    Health `json:"health"`
	Missing   bool   `json:"missing,omitempty"`
}

type HelmMap struct {
	Releases []HelmMapRelease `json:"releases"`
	Warnings []string         `json:"warnings,omitempty"`
}

// HelmMap reads every release in namespace ("" = all) and the state of what it made.
func (c *Cluster) HelmMap(ctx context.Context, namespace string) (*HelmMap, error) {
	raws, err := c.helmSecrets(ctx, namespace, "")
	if err != nil {
		return nil, err
	}
	kinds, _ := c.Kinds()
	byKind := map[string]Kind{} // "apps/Deployment" -> Kind
	for _, k := range kinds {
		byKind[k.Group+"/"+k.Kind] = k
	}
	out := &HelmMap{Releases: []HelmMapRelease{}}
	seen := map[string]bool{}
	var current []*helmRaw
	for _, r := range raws {
		if key := r.Namespace + "/" + r.Name; !seen[key] {
			seen[key] = true
			current = append(current, r)
		}
	}
	// Live state: one List per (kind, namespace) pair, shared by all releases.
	type listKey struct{ kind, ns string }
	need := map[listKey]Kind{}
	parsed := make([][]*unstructured.Unstructured, len(current))
	for i, r := range current {
		objs, _ := parseManifests(r.Manifest)
		parsed[i] = objs
		for _, o := range objs {
			k, ok := byKind[groupOf(o.GetAPIVersion())+"/"+o.GetKind()]
			if !ok {
				continue
			}
			ns := ""
			if k.Namespaced {
				ns = o.GetNamespace()
				if ns == "" {
					ns = r.Namespace
				}
			}
			need[listKey{k.ID(), ns}] = k
		}
	}
	var mu sync.Mutex
	live := map[string]Row{} // kindID/ns/name -> row
	listed := map[listKey]bool{}
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for key, k := range need {
		wg.Add(1)
		go func(key listKey, k Kind) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			rows, _, err := c.List(lctx, k, key.ns)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				out.Warnings = append(out.Warnings, k.Kind+" in "+orAll(key.ns)+": "+firstLine(err.Error()))
				return
			}
			listed[key] = true
			for _, r := range rows {
				live[k.ID()+"/"+r.Namespace+"/"+r.Name] = r
			}
		}(key, k)
	}
	wg.Wait()

	for i, r := range current {
		mr := HelmMapRelease{HelmRelease: r.summary(), Objects: []HelmObject{}}
		for _, o := range parsed[i] {
			ho := HelmObject{Kind: o.GetKind(), Name: o.GetName()}
			k, served := byKind[groupOf(o.GetAPIVersion())+"/"+o.GetKind()]
			if served {
				ho.KindID = k.ID()
				if k.Namespaced {
					ho.Namespace = o.GetNamespace()
					if ho.Namespace == "" {
						ho.Namespace = r.Namespace
					}
				}
				key := listKey{k.ID(), ho.Namespace}
				if row, ok := live[k.ID()+"/"+ho.Namespace+"/"+ho.Name]; ok {
					ho.Status, ho.Health = row.Status, row.Health
					if ho.Status == "" || ho.Status == "-" {
						ho.Status = "Exists"
					}
					if ho.Health == "" || ho.Health == Neutral {
						ho.Health = Healthy // it exists, and its kind has no health of its own
					}
				} else if listed[key] {
					ho.Status, ho.Health, ho.Missing = "Missing", Failing, true
				} else {
					ho.Status = "Unknown"
				}
			} else {
				ho.Status = "Kind not served"
				ho.Health = Warning
			}
			mr.Objects = append(mr.Objects, ho)
			mr.Counts.Total++
			switch {
			case ho.Missing:
				mr.Counts.Missing++
			case ho.Health == Failing:
				mr.Counts.Failing++
			case ho.Health == Warning:
				mr.Counts.Warning++
			}
		}
		sort.SliceStable(mr.Objects, func(a, b int) bool {
			ra, rb := kindRank(mr.Objects[a].Kind), kindRank(mr.Objects[b].Kind)
			if ra != rb {
				return ra < rb
			}
			if mr.Objects[a].Kind != mr.Objects[b].Kind {
				return mr.Objects[a].Kind < mr.Objects[b].Kind
			}
			return mr.Objects[a].Name < mr.Objects[b].Name
		})
		// A release is as healthy as the worst of what it made (a "deployed" release
		// whose Deployment crash-loops is not healthy).
		switch {
		case mr.Health == Failing || mr.Counts.Missing > 0 || mr.Counts.Failing > 0:
			mr.Health = Failing
		case mr.Health == Warning || mr.Counts.Warning > 0:
			mr.Health = Warning
		}
		out.Releases = append(out.Releases, mr)
	}
	sort.Strings(out.Warnings)
	return out, nil
}

func groupOf(apiVersion string) string {
	if i := strings.Index(apiVersion, "/"); i >= 0 {
		return apiVersion[:i]
	}
	return ""
}

func orAll(ns string) string {
	if ns == "" {
		return "the cluster"
	}
	return ns
}

// kindRank orders a release's objects the way people read them: traffic, then
// workloads, then config, then the rest (RBAC, CRDs...).
func kindRank(kind string) int {
	switch kind {
	case "Ingress", "Gateway", "HTTPRoute":
		return 0
	case "Service":
		return 1
	case "Deployment", "StatefulSet", "DaemonSet", "CronJob", "Job", "Rollout":
		return 2
	case "HorizontalPodAutoscaler", "ScaledObject", "PodDisruptionBudget":
		return 3
	case "ConfigMap", "Secret", "PersistentVolumeClaim", "ExternalSecret":
		return 4
	case "ServiceAccount", "Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding":
		return 6
	case "CustomResourceDefinition":
		return 7
	}
	return 5
}
