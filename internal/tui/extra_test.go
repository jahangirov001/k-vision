package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/njahangirov/kube-console/internal/kube"
)

func TestClusterTopologyText(t *testing.T) {
	cm := &kube.ClusterMap{Nodes: 2, Pods: 3,
		Pools: []kube.MapPool{{Name: "apps", Source: "karpenter", CPUReq: 500, CPUAlloc: 1000, MemReq: 1 << 30, MemAlloc: 4 << 30, Pods: 3, Health: kube.Healthy,
			Types: []string{"m5.large"}, Nodes: []kube.MapNode{{Name: "n1", Health: kube.Healthy, CPUPct: 95}, {Name: "n2", Health: kube.Failing, Status: "NotReady"}}}},
		Namespaces: []kube.MapNamespace{{Name: "shop", Pods: 2, Workloads: 1, Failing: 1, Health: kube.Failing}, {Name: "kube-system", Pods: 1, System: true, Health: kube.Healthy}},
		Links:      []kube.MapLink{{Namespace: "shop", Pool: "apps", Pods: 2}}}
	out := stripANSI(clusterTopologyText(cm, false))
	for _, want := range []string{"apps", "2 nodes", "50%", "shop", "1 failing", "apps ×2", "n2 NotReady", "1 platform namespaces hidden"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "kube-system (platform)") {
		t.Error("platform namespace shown without +system")
	}
	if !strings.Contains(stripANSI(clusterTopologyText(cm, true)), "kube-system (platform)") {
		t.Error("+system should show kube-system")
	}
}

func TestNamespaceTopologyText(t *testing.T) {
	g := &kube.Graph{Nodes: []kube.GraphNode{
		{ID: "Ingress/web", Kind: "Ingress", Name: "web", Col: 0, Health: kube.Healthy},
		{ID: "Service/web", Kind: "Service", Name: "web", Col: 1, Health: kube.Healthy},
		{ID: "Deployment/web", Kind: "Deployment", Name: "web", Col: 2, Health: kube.Warning, Status: "1/2 ready",
			Pods: []kube.GraphPod{{Health: kube.Healthy}, {Health: kube.Failing}}, Badges: []kube.GraphBadge{{Text: "HPA 2–5"}}},
		{ID: "Secret/db", Kind: "Secret", Name: "db", Col: 3, Missing: true, Health: kube.Failing, Status: "Missing"},
		{ID: "ConfigMap/lonely", Kind: "ConfigMap", Name: "lonely", Col: 3},
	}, Edges: []kube.GraphEdge{{From: "Ingress/web", To: "Service/web"}, {From: "Service/web", To: "Deployment/web"}, {From: "Deployment/web", To: "Secret/db"}}}
	out := stripANSI(namespaceTopologyText(g))
	lines := strings.Split(out, "\n")
	idx := func(s string) int {
		for i, l := range lines {
			if strings.Contains(l, s) {
				return i
			}
		}
		return -1
	}
	if !(idx("Ingress web") < idx("Service web") && idx("Service web") < idx("Deployment web") && idx("Deployment web") < idx("Secret db")) {
		t.Errorf("chain out of order:\n%s", out)
	}
	for _, want := range []string{"└─ ", "[HPA 2–5]", "Missing", "ConfigMap lonely", "2 to look at"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestHelmText(t *testing.T) {
	rs := []kube.HelmRelease{{Name: "api", Namespace: "shop", Revision: 3, Status: "deployed", Chart: "api", ChartVersion: "1.2.0", AppVersion: "2.0", Updated: time.Now().Add(-2 * time.Hour), Health: kube.Healthy}}
	if out := stripANSI(helmReleasesText(rs)); !strings.Contains(out, "api-1.2.0") || !strings.Contains(out, "2h") {
		t.Errorf("releases:\n%s", out)
	}
	d := &kube.HelmDetail{HelmRelease: rs[0], Values: "", AllValues: "replicas: 2\n", History: rs}
	if out := stripANSI(helmDetailText(d, "summary")); !strings.Contains(out, "defaults apply") || !strings.Contains(out, ":helm shop/api values") {
		t.Errorf("summary:\n%s", out)
	}
	if out := helmDetailText(d, "all"); !strings.Contains(out, "replicas: 2") {
		t.Errorf("all values: %s", out)
	}
}

func TestColorLogKeepsText(t *testing.T) {
	for _, l := range []string{
		`2026-11-03T10:00:00Z ERROR failed to connect url=https://db:5432 took=12ms "quoted"`,
		`GET /health 503 1.2ms`, `level=info msg="ok" count=3`, `plain line`,
	} {
		if got := stripANSI(colorLog(l)); strings.TrimLeft(got, " ▌") != l {
			t.Errorf("colorLog changed the text:\n got %q\nwant %q", got, l)
		}
	}
	if !strings.HasPrefix(stripANSI(colorLog("ERROR boom")), "▌") {
		t.Error("error lines get a marker")
	}
}

func TestTemplatesCoverMenu(t *testing.T) {
	if len(kube.Templates) < 19 {
		t.Fatalf("only %d templates", len(kube.Templates))
	}
	y := kube.TemplateFor(kube.Kind{Name: "deployments", Group: "apps", Kind: "Deployment"}, "shop")
	if !strings.Contains(y, "namespace: shop") || strings.Contains(y, "NAMESPACE") {
		t.Errorf("namespace not filled:\n%s", y)
	}
	g := kube.TemplateFor(kube.Kind{Name: "widgets", Group: "acme.io", Version: "v1", Kind: "Widget", Namespaced: true}, "x")
	if !strings.Contains(g, "apiVersion: acme.io/v1") || !strings.Contains(g, "kind: Widget") {
		t.Errorf("generic skeleton:\n%s", g)
	}
}

func TestTsLess(t *testing.T) {
	if tsLess("2026-11-03T10:00:00.9Z", "2026-11-03T10:00:00.10Z") {
		t.Error(".9s is after .10s")
	}
}

func TestHelmTopologyText(t *testing.T) {
	r := kube.HelmMapRelease{HelmRelease: kube.HelmRelease{Name: "api", Namespace: "shop", Chart: "api", ChartVersion: "1.0", Revision: 2, Status: "deployed", Health: kube.Failing}}
	r.Objects = []kube.HelmObject{
		{Kind: "Service", Namespace: "shop", Name: "api", Status: "Exists", Health: kube.Healthy},
		{Kind: "Deployment", Namespace: "shop", Name: "api", Status: "1/2 ready", Health: kube.Warning},
		{Kind: "ConfigMap", Namespace: "shop", Name: "a", Health: kube.Healthy}, {Kind: "ConfigMap", Namespace: "shop", Name: "b", Health: kube.Healthy},
		{Kind: "ConfigMap", Namespace: "shop", Name: "c", Health: kube.Healthy}, {Kind: "ConfigMap", Namespace: "shop", Name: "gone", Missing: true, Health: kube.Failing},
	}
	r.Counts.Total = 6
	out := stripANSI(helmTopologyText(&kube.HelmMap{Releases: []kube.HelmMapRelease{r}}))
	for _, want := range []string{"shop/api", "api-1.0 · rev 2", "Deployment", "1/2 ready", "ConfigMap ×4", "✕ shop/gone", "missing from the cluster", "1 to look at"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestCostText(t *testing.T) {
	rep := &kube.CostReport{Source: "opencost/opencost", Window: "7d", Aggregate: "namespace", Total: 140, Idle: 40, Monthly: 600, Waste: 30,
		Rows:     []kube.CostRow{{Name: "shop", Total: 70, CPUEff: 0.1, RAMEff: 0.6, Waste: 40}, {Name: "__unallocated__", Total: 0.5}},
		Clusters: []kube.CostRow{{Name: "a", Total: 140}, {Name: "b", Total: 10}}}
	out := stripANSI(costText(rep))
	for _, want := range []string{"$140", "$600", "$20.00", "$40.00", "shop", "▇", "10%", "60%", "unallocated", "sees 2 clusters", "$150"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
