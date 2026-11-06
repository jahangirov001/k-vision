package kube

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ArgoDashboard is the Argo CD page: what Argo CD's own UI shows on its first screen,
// read from the Application, ApplicationSet and AppProject objects -- no Argo CD API
// or login needed.
type ArgoDashboard struct {
	Apps         int            `json:"apps"`
	AppSets      int            `json:"appSets"`
	Projects     int            `json:"projects"`
	Health       map[string]int `json:"health"` // Healthy, Progressing, Degraded, Missing, Suspended, Unknown
	Sync         map[string]int `json:"sync"`   // Synced, OutOfSync, Unknown
	AutoSync     int            `json:"autoSync"`
	Attention    []ArgoApp      `json:"attention"` // degraded / missing / out of sync / failed sync, worst first
	Recent       []ArgoApp      `json:"recent"`    // last finished syncs, newest first
	ByProject    []ArgoCount    `json:"byProject"`
	ByDest       []ArgoCount    `json:"byDestination"`
	ByRepo       []ArgoCount    `json:"byRepo"`
	AppSetStatus []ArgoAppSet   `json:"appSetList"`
	Installed    bool           `json:"installed"`
}

type ArgoApp struct {
	Namespace   string    `json:"namespace"`
	Name        string    `json:"name"`
	Project     string    `json:"project"`
	Health      string    `json:"health"`
	Sync        string    `json:"sync"`
	Operation   string    `json:"operation"` // last sync: Succeeded, Failed, Error, Running
	Message     string    `json:"message,omitempty"`
	Revision    string    `json:"revision"`
	Destination string    `json:"destination"`
	Repo        string    `json:"repo"`
	Finished    time.Time `json:"finished"`
	AutoSync    bool      `json:"autoSync"`
	Level       Health    `json:"level"`
}

type ArgoCount struct {
	Name    string `json:"name"`
	Apps    int    `json:"apps"`
	Healthy int    `json:"healthy"`
	Bad     int    `json:"bad"`
}

type ArgoAppSet struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Apps      int    `json:"apps"`
	Status    string `json:"status"`
	Health    Health `json:"health"`
}

var (
	argoApps     = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	argoAppSets  = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applicationsets"}
	argoProjects = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "appprojects"}
)

func (c *Cluster) ArgoDashboard(ctx context.Context) (*ArgoDashboard, error) {
	d := &ArgoDashboard{Health: map[string]int{}, Sync: map[string]int{}, Attention: []ArgoApp{}, Recent: []ArgoApp{},
		ByProject: []ArgoCount{}, ByDest: []ArgoCount{}, ByRepo: []ArgoCount{}, AppSetStatus: []ArgoAppSet{}}
	var wg sync.WaitGroup
	var apps, sets, projs []map[string]any
	var appErr error
	list := func(gvr schema.GroupVersionResource, into *[]map[string]any, errp *error) {
		defer wg.Done()
		l, err := c.Dynamic.Resource(gvr).List(ctx, metav1.ListOptions{})
		if err != nil {
			if errp != nil {
				*errp = err
			}
			return
		}
		for _, it := range l.Items {
			*into = append(*into, it.Object)
		}
	}
	wg.Add(3)
	go list(argoApps, &apps, &appErr)
	go list(argoAppSets, &sets, nil)
	go list(argoProjects, &projs, nil)
	wg.Wait()
	if appErr != nil {
		if strings.Contains(appErr.Error(), "the server could not find the requested resource") {
			return d, nil // no Argo CD here: Installed stays false
		}
		return nil, appErr
	}
	d.Installed = true
	d.Apps, d.AppSets, d.Projects = len(apps), len(sets), len(projs)

	proj, dest, repo := map[string]*ArgoCount{}, map[string]*ArgoCount{}, map[string]*ArgoCount{}
	count := func(m map[string]*ArgoCount, k string, a ArgoApp) {
		x := m[k]
		if x == nil {
			x = &ArgoCount{Name: k}
			m[k] = x
		}
		x.Apps++
		switch a.Level {
		case Healthy:
			x.Healthy++
		case Failing, Warning:
			x.Bad++
		}
	}
	var all []ArgoApp
	for _, o := range apps {
		meta, _ := o["metadata"].(map[string]any)
		a := ArgoApp{Namespace: str(meta, "namespace"), Name: str(meta, "name"), Project: str(o, "spec", "project"),
			Health: orUnknown(str(o, "status", "health", "status")), Sync: orUnknown(str(o, "status", "sync", "status")),
			Operation: str(o, "status", "operationState", "phase"), Message: firstLine(str(o, "status", "operationState", "message")),
			AutoSync: lookup(o, "spec", "syncPolicy", "automated") != nil}
		rev := str(o, "status", "sync", "revision")
		if rev == "" {
			if rs := sliceAt(o, "status", "sync", "revisions"); len(rs) > 0 {
				rev = fmt.Sprint(rs[0])
			}
		}
		if len(rev) > 8 {
			rev = rev[:8]
		}
		a.Revision = rev
		srv := firstNonEmpty(str(o, "spec", "destination", "name"), str(o, "spec", "destination", "server"))
		if srv == "https://kubernetes.default.svc" {
			srv = "in-cluster"
		}
		a.Destination = srv + " / " + orDash(str(o, "spec", "destination", "namespace"))
		r := str(o, "spec", "source", "repoURL")
		if r == "" {
			if ss := sliceAt(o, "spec", "sources"); len(ss) > 0 {
				m, _ := ss[0].(map[string]any)
				r = str(m, "repoURL")
			}
		}
		a.Repo = shortRepo(r)
		if t, err := time.Parse(time.RFC3339, str(o, "status", "operationState", "finishedAt")); err == nil {
			a.Finished = t
		}
		switch {
		case a.Health == "Degraded" || a.Health == "Missing" || a.Operation == "Failed" || a.Operation == "Error":
			a.Level = Failing
		case a.Sync == "OutOfSync" || a.Health == "Progressing" || a.Health == "Unknown" || a.Health == "Suspended":
			a.Level = Warning
		case a.Health == "Healthy" && a.Sync == "Synced":
			a.Level = Healthy
		}
		d.Health[a.Health]++
		d.Sync[a.Sync]++
		if a.AutoSync {
			d.AutoSync++
		}
		count(proj, orDash(a.Project), a)
		count(dest, srv, a)
		count(repo, a.Repo, a)
		all = append(all, a)
	}
	rank := map[Health]int{Failing: 0, Warning: 1}
	for _, a := range all {
		if a.Level == Failing || a.Level == Warning {
			d.Attention = append(d.Attention, a)
		}
	}
	sort.SliceStable(d.Attention, func(i, j int) bool {
		if rank[d.Attention[i].Level] != rank[d.Attention[j].Level] {
			return rank[d.Attention[i].Level] < rank[d.Attention[j].Level]
		}
		return d.Attention[i].Name < d.Attention[j].Name
	})
	recent := append([]ArgoApp{}, all...)
	sort.Slice(recent, func(i, j int) bool { return recent[i].Finished.After(recent[j].Finished) })
	for _, a := range recent {
		if a.Finished.IsZero() || len(d.Recent) >= 12 {
			break
		}
		d.Recent = append(d.Recent, a)
	}
	flat := func(m map[string]*ArgoCount) []ArgoCount {
		out := make([]ArgoCount, 0, len(m))
		for _, v := range m {
			out = append(out, *v)
		}
		sort.Slice(out, func(i, j int) bool {
			return out[i].Apps > out[j].Apps || (out[i].Apps == out[j].Apps && out[i].Name < out[j].Name)
		})
		return out
	}
	d.ByProject, d.ByDest, d.ByRepo = flat(proj), flat(dest), flat(repo)
	for _, o := range sets {
		meta, _ := o["metadata"].(map[string]any)
		r := Row{Cells: map[string]string{}}
		appSetRow(&r, o)
		n := 0
		fmt.Sscanf(r.Cells["APPLICATIONS"], "%d", &n)
		d.AppSetStatus = append(d.AppSetStatus, ArgoAppSet{Namespace: str(meta, "namespace"), Name: str(meta, "name"), Apps: n, Status: r.Status, Health: r.Health})
	}
	sort.Slice(d.AppSetStatus, func(i, j int) bool { return d.AppSetStatus[i].Name < d.AppSetStatus[j].Name })
	return d, nil
}

func orUnknown(s string) string {
	if s == "" {
		return "Unknown"
	}
	return s
}
