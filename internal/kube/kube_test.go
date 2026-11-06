package kube

import (
	"bytes"

	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"k8s.io/apimachinery/pkg/api/resource"

	kubefake "k8s.io/client-go/kubernetes/fake"
	"os"

	"k8s.io/client-go/tools/clientcmd"
	"regexp"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func obj(o map[string]any) *unstructured.Unstructured { return &unstructured.Unstructured{Object: o} }

func TestPodRowHealth(t *testing.T) {
	k := Kind{Name: "pods", Version: "v1", Namespaced: true}
	cases := []struct {
		name   string
		status map[string]any
		want   string
		health Health
	}{
		{"running", map[string]any{"phase": "Running", "containerStatuses": []any{
			map[string]any{"ready": true, "restartCount": int64(0), "state": map[string]any{"running": map[string]any{}}}}},
			"Running", Healthy},
		{"crashloop", map[string]any{"phase": "Running", "containerStatuses": []any{
			map[string]any{"ready": false, "restartCount": int64(7), "state": map[string]any{
				"waiting": map[string]any{"reason": "CrashLoopBackOff"}}}}},
			"CrashLoopBackOff", Failing},
		{"pending", map[string]any{"phase": "Pending"}, "Pending", Warning},
		{"not ready", map[string]any{"phase": "Running", "containerStatuses": []any{
			map[string]any{"ready": false, "state": map[string]any{"running": map[string]any{}}}}},
			"Running", Warning},
	}
	for _, c := range cases {
		r := toRow(k, obj(map[string]any{
			"metadata": map[string]any{"name": "p", "namespace": "ns"},
			"spec":     map[string]any{"containers": []any{map[string]any{"name": "app"}}},
			"status":   c.status,
		}))
		if r.Status != c.want || r.Health != c.health {
			t.Errorf("%s: got %s/%q, want %s/%q", c.name, r.Status, r.Health, c.want, c.health)
		}
	}
}

func TestArgoAndCrossplaneRows(t *testing.T) {
	argo := toRow(Kind{Name: "applications", Group: "argoproj.io", Version: "v1alpha1"}, obj(map[string]any{
		"metadata": map[string]any{"name": "scoring"},
		"status": map[string]any{"sync": map[string]any{"status": "OutOfSync", "revision": "abcdef123456"},
			"health": map[string]any{"status": "Degraded"}},
	}))
	if argo.Health != Failing || argo.Cells["REVISION"] != "abcdef12" {
		t.Errorf("argo row: %+v", argo)
	}
	xp := toRow(Kind{Name: "buckets", Group: "s3.aws.upbound.io", Version: "v1beta1"}, obj(map[string]any{
		"metadata": map[string]any{"name": "b"},
		"status": map[string]any{"conditions": []any{
			map[string]any{"type": "Ready", "status": "True"},
			map[string]any{"type": "Synced", "status": "False"}}},
	}))
	if xp.Status != "NotSynced" || xp.Health != Failing {
		t.Errorf("crossplane row: %+v", xp)
	}
}

func TestExplainUnschedulable(t *testing.T) {
	p := &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodPending}}
	ev := corev1.Event{Reason: "FailedScheduling", LastTimestamp: metav1.NewTime(time.Now()),
		Message: "0/5 nodes are available: 3 Insufficient cpu, 2 Insufficient memory."}
	got := strings.Join(explainPod(p, []corev1.Event{ev}), " ")
	if !strings.Contains(got, "cpu, memory") || !strings.Contains(got, "NodePool") {
		t.Errorf("explain: %s", got)
	}
}

func TestExplainOOMCrashLoop(t *testing.T) {
	p := &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
		Name: "api", RestartCount: 4,
		State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}},
	}}}}
	got := strings.Join(explainPod(p, nil), " ")
	if !strings.Contains(got, "out of memory") || !strings.Contains(got, "137") {
		t.Errorf("explain: %s", got)
	}
}

func TestMatches(t *testing.T) {
	r := Row{Namespace: "argocd", Name: "repo-server-1", Status: "CrashLoopBackOff", Health: Failing,
		Labels: map[string]string{"app": "repo"}}
	for f, want := range map[string]bool{"": true, "repo": true, "argocd/": true, "!ok": true,
		"app=repo": true, "app=web": false, "nginx": false, "crashloop": true} {
		if Matches(r, f) != want {
			t.Errorf("Matches(%q) = %v", f, !want)
		}
	}
	if Matches(Row{Health: Healthy}, "!ok") {
		t.Error("healthy row matched !ok")
	}
}

func TestHumanAge(t *testing.T) {
	for d, want := range map[time.Duration]string{30 * time.Second: "30s", 5 * time.Minute: "5m",
		3 * time.Hour: "3h", 72 * time.Hour: "3d"} {
		if got := HumanAge(time.Now().Add(-d)); got != want {
			t.Errorf("%v: %s want %s", d, got, want)
		}
	}
}

func TestProtectedByDefault(t *testing.T) {
	m := &Manager{safe: regexpMust(DefaultSafe)}
	for ctx, want := range map[string]bool{
		"arn:aws:eks:eu-central-1:111111111111:cluster/shop-prod": true,
		"arn:aws:eks:eu-central-1:222222222222:cluster/ops-prod":  true,
		"ops-prod":  true,
		"ml-trials": true,
		"arn:aws:eks:eu-central-1:333333333333:cluster/payments-dev": true,
		"arn:aws:eks:eu-central-1:333333333333:cluster/shared-dev":   true,
		"staging-api":    true,
		"kind-local":     false,
		"minikube":       false,
		"docker-desktop": false,
		"k3d-mycluster":  false,
		"minikube-prod":  true,
	} {
		if got := m.Protected(ctx); got != want {
			t.Errorf("Protected(%s) = %v, want %v", ctx, got, want)
		}
	}
}

func regexpMust(p string) *regexp.Regexp { return regexp.MustCompile(p) }

func TestExplainFailedJobRun(t *testing.T) {
	p := &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodFailed, ContainerStatuses: []corev1.ContainerStatus{{
		Name: "geoimport",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1,
			FinishedAt: metav1.NewTime(time.Now().Add(-3 * time.Hour))}},
	}}}}
	got := strings.Join(explainPod(p, nil), " ")
	if !strings.Contains(got, "exited with code 1") || !strings.Contains(got, "logs") {
		t.Errorf("explain: %s", got)
	}
}

func TestNodeShellPodName(t *testing.T) {
	got := trimName("ip-10-136-187-77.eu-central-1.compute.internal", 40)
	if got != "ip-10-136-187-77" {
		t.Errorf("trimName: %q", got)
	}
	long := trimName(strings.Repeat("a", 80), 40)
	if len(long) != 40 {
		t.Errorf("trimName did not cap: %d", len(long))
	}
}

func TestHideContext(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KV_AUDIT_LOG", dir+"/audit.log")
	m := &Manager{safe: regexpMust(DefaultSafe), hidden: map[string]bool{}, clusters: map[string]*Cluster{}}
	m.raw.Contexts = map[string]*clientcmdapi.Context{"a": {}, "b": {}, "cur": {}}
	m.raw.CurrentContext = "cur"
	if err := m.SetHidden("b", true); err != nil {
		t.Fatal(err)
	}
	if err := m.SetHidden("cur", true); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range m.Contexts() {
		names = append(names, c.Name)
	}
	if strings.Join(names, ",") != "a,cur" {
		t.Fatalf("shown after hiding b and the current one: %v", names)
	}
	if got := loadHidden(); !got["b"] || !got["cur"] {
		t.Fatalf("not persisted: %v", got)
	}
	if err := m.SetHidden("b", false); err != nil || len(m.Contexts()) != 3 {
		t.Fatalf("show again: %v %d", err, len(m.Contexts()))
	}
	if err := m.SetHidden("nope", true); err == nil {
		t.Fatal("hiding an unknown context should fail")
	}
}

func TestPodsOfUsesTheSelector(t *testing.T) {
	scheme := runtime.NewScheme()
	pod := func(name, app string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod",
			"metadata": map[string]any{"name": name, "namespace": "ns", "labels": map[string]any{"app": app}}}}
	}
	dep := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "web", "namespace": "ns"},
		"spec":     map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "web"}}}}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		{Version: "v1", Resource: "pods"}:                       "PodList",
		{Group: "apps", Version: "v1", Resource: "deployments"}: "DeploymentList",
	}, pod("web-1", "web"), pod("web-2", "web"), pod("db-1", "db"), dep)
	c := &Cluster{Dynamic: dyn}
	rows, err := c.PodsOf(context.Background(), Kind{Name: "deployments", Group: "apps", Version: "v1", Namespaced: true}, "ns", "web")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range rows {
		names = append(names, r.Name)
	}
	if strings.Join(names, ",") != "web-1,web-2" {
		t.Fatalf("pods of web: %v", names)
	}
}

// The real kubeconfig is never touched: KUBECONFIG points at a temp copy.
func TestDeleteFromKubeconfig(t *testing.T) {
	dir := t.TempDir()
	kv := dir + "/config"
	const cfg = `apiVersion: v1
kind: Config
current-context: keep
clusters:
- {name: c-keep, cluster: {server: "https://k"}}
- {name: c-shared, cluster: {server: "https://s"}}
users:
- {name: u-keep, user: {token: a}}
- {name: u-shared, user: {token: b}}
contexts:
- {name: keep, context: {cluster: c-keep, user: u-keep}}
- {name: trials, context: {cluster: c-shared, user: u-shared}}
- {name: trials-runner, context: {cluster: c-shared, user: u-shared}}
`
	if err := os.WriteFile(kv, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", kv)
	t.Setenv("KV_AUDIT_LOG", dir+"/audit.log")
	m, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.DeleteFromKubeconfig("keep"); err == nil {
		t.Fatal("deleting the current context should be refused")
	}
	backup, err := m.DeleteFromKubeconfig("trials-runner")
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(backup); string(data) != cfg {
		t.Fatal("backup is not the original file")
	}
	after, _ := clientcmd.LoadFromFile(kv)
	if _, ok := after.Contexts["trials-runner"]; ok {
		t.Fatal("context still there")
	}
	if _, ok := after.Clusters["c-shared"]; !ok {
		t.Fatal("shared cluster removed while another context still uses it")
	}
	if _, err := m.DeleteFromKubeconfig("trials"); err != nil {
		t.Fatal(err)
	}
	after, _ = clientcmd.LoadFromFile(kv)
	if _, ok := after.Clusters["c-shared"]; ok {
		t.Fatal("cluster left behind after its last context was removed")
	}
	if _, ok := after.AuthInfos["u-shared"]; ok {
		t.Fatal("credentials left behind after their last context was removed")
	}
	if len(after.Contexts) != 1 || after.CurrentContext != "keep" {
		t.Fatalf("other contexts disturbed: %v current=%s", after.Contexts, after.CurrentContext)
	}
	for _, c := range m.AllContexts() {
		if c.Name != "keep" {
			t.Fatalf("manager still lists %s", c.Name)
		}
	}
}

func helmSecret(t *testing.T, ns, name string, rev int, status, chartVer string, values map[string]any) *corev1.Secret {
	t.Helper()
	raw := map[string]any{"name": name, "namespace": ns, "version": rev,
		"info": map[string]any{"status": status, "last_deployed": time.Now().Add(-time.Duration(10-rev) * time.Hour).Format(time.RFC3339), "notes": "hello"},
		"chart": map[string]any{"metadata": map[string]any{"name": "web", "version": chartVer, "appVersion": "1.0"},
			"values": map[string]any{"replicas": 1, "image": map[string]any{"tag": "a", "pull": "IfNotPresent"}}},
		"config": values, "manifest": "kind: Deployment\n"}
	j, _ := json.Marshal(raw)
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	_, _ = w.Write(j)
	_ = w.Close()
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("sh.helm.release.v1.%s.v%d", name, rev), Namespace: ns,
			Labels: map[string]string{"owner": "helm", "name": name, "version": fmt.Sprint(rev)}},
		Type: "helm.sh/release.v1",
		Data: map[string][]byte{"release": []byte(base64.StdEncoding.EncodeToString(gz.Bytes()))},
	}
}

func TestHelmReleasesFromSecrets(t *testing.T) {
	cs := kubefake.NewSimpleClientset(
		helmSecret(t, "apps", "web", 1, "superseded", "1.0.0", nil),
		helmSecret(t, "apps", "web", 2, "deployed", "1.1.0", map[string]any{"image": map[string]any{"tag": "b"}}),
		helmSecret(t, "ops", "web-two", 1, "failed", "1.0.0", nil),
	)
	c := &Cluster{Typed: cs}
	rels, err := c.HelmReleases(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 2 || rels[0].Name != "web" || rels[0].Revision != 2 || rels[0].ChartVersion != "1.1.0" || rels[0].Health != Healthy {
		t.Fatalf("releases: %+v", rels)
	}
	if rels[1].Health != Failing {
		t.Fatalf("failed release not failing: %+v", rels[1])
	}
	charts, _ := c.HelmCharts(context.Background(), "")
	if len(charts) != 1 || strings.Join(charts[0].Versions, ",") != "1.0.0,1.1.0" || len(charts[0].Releases) != 2 {
		t.Fatalf("charts: %+v", charts)
	}
	d, err := c.HelmRelease(context.Background(), "apps", "web", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.History) != 2 || !strings.Contains(d.Values, "tag: b") || !strings.Contains(d.AllValues, "pull: IfNotPresent") ||
		!strings.Contains(d.AllValues, "tag: b") || d.Notes != "hello" {
		t.Fatalf("detail: %+v", d)
	}
	old, _ := c.HelmRelease(context.Background(), "apps", "web", 1)
	if old.Revision != 1 || old.ChartVersion != "1.0.0" {
		t.Fatalf("revision 1: %+v", old.HelmRelease)
	}
}

func TestParseManifests(t *testing.T) {
	objs, err := parseManifests(`
apiVersion: v1
kind: ConfigMap
metadata: {name: a}
---
# a comment-only document
---
apiVersion: v1
kind: List
items:
- {apiVersion: v1, kind: Secret, metadata: {name: b}}
- {apiVersion: v1, kind: Service, metadata: {name: c}}
---
{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"name": "d"}}
`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, o := range objs {
		got = append(got, o.GetKind()+"/"+o.GetName())
	}
	if strings.Join(got, ",") != "ConfigMap/a,Secret/b,Service/c,Deployment/d" {
		t.Fatalf("parsed: %v", got)
	}
	if _, err := parseManifests("kind: [unclosed"); err == nil {
		t.Fatal("bad YAML should fail")
	}
}

func TestTarRoundTrip(t *testing.T) {
	buf, err := TarOf(map[string][]byte{"config.yaml": []byte("a: 1\n")})
	if err != nil {
		t.Fatal(err)
	}
	name, data, ok := SingleFile(buf.Bytes())
	if !ok || name != "config.yaml" || string(data) != "a: 1\n" {
		t.Fatalf("single file: %v %q %q", ok, name, data)
	}
	two, _ := TarOf(map[string][]byte{"a": []byte("1"), "b": []byte("2")})
	if _, _, ok := SingleFile(two.Bytes()); ok {
		t.Fatal("two files read as one")
	}
	if _, err := TarOf(map[string][]byte{"../../etc/passwd": []byte("x")}); err == nil {
		t.Fatal("path escaping the target dir was accepted")
	}
	if cleanPath("") != "/" || cleanPath("../etc") != "/etc" || cleanPath("/var/log/") != "/var/log" {
		t.Fatal("cleanPath")
	}
}

func TestGraphLinks(t *testing.T) {
	u := func(o map[string]any) *unstructured.Unstructured { return &unstructured.Unstructured{Object: o} }
	tr := true
	dep := u(map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "web", "namespace": "ns"},
		"spec": map[string]any{"replicas": int64(2), "selector": map[string]any{"matchLabels": map[string]any{"app": "web"}},
			"template": map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": "web", "tier": "fe"}},
				"spec": map[string]any{"containers": []any{map[string]any{"name": "c", "image": "nginx",
					"envFrom": []any{map[string]any{"configMapRef": map[string]any{"name": "web-cfg"}}}}},
					"volumes": []any{map[string]any{"name": "s", "secret": map[string]any{"secretName": "web-tls"}}}}}},
		"status": map[string]any{"readyReplicas": int64(2)}})
	rs := u(map[string]any{"apiVersion": "apps/v1", "kind": "ReplicaSet", "metadata": map[string]any{"name": "web-1", "namespace": "ns",
		"ownerReferences": []any{map[string]any{"kind": "Deployment", "name": "web", "controller": tr, "apiVersion": "apps/v1", "uid": "1"}}},
		"spec": map[string]any{"replicas": int64(2)}})
	pod := u(map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "web-1-abc", "namespace": "ns",
		"labels":          map[string]any{"app": "web", "tier": "fe"},
		"ownerReferences": []any{map[string]any{"kind": "ReplicaSet", "name": "web-1", "controller": tr, "apiVersion": "apps/v1", "uid": "2"}}},
		"spec":   map[string]any{"containers": []any{map[string]any{"name": "c"}}},
		"status": map[string]any{"phase": "Running", "containerStatuses": []any{map[string]any{"ready": true, "state": map[string]any{"running": map[string]any{}}}}}})
	svc := u(map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "web", "namespace": "ns"},
		"spec": map[string]any{"type": "ClusterIP", "selector": map[string]any{"app": "web"}, "ports": []any{map[string]any{"port": int64(80), "protocol": "TCP"}}}})
	orphanSvc := u(map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "ghost", "namespace": "ns"},
		"spec": map[string]any{"selector": map[string]any{"app": "nothing"}}})
	ing := u(map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": "Ingress", "metadata": map[string]any{"name": "web", "namespace": "ns"},
		"spec": map[string]any{"rules": []any{map[string]any{"host": "web.example.com", "http": map[string]any{"paths": []any{
			map[string]any{"backend": map[string]any{"service": map[string]any{"name": "web"}}},
			map[string]any{"backend": map[string]any{"service": map[string]any{"name": "gone"}}}}}}}}})
	cm := u(map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "web-cfg", "namespace": "ns"}, "data": map[string]any{"a": "1"}})
	scheme := runtime.NewScheme()
	lists := map[schema.GroupVersionResource]string{}
	for _, s := range graphSources {
		lists[schema.GroupVersionResource{Group: s.group, Version: s.version, Resource: s.resource}] = "List"
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, lists, dep, rs, pod, svc, orphanSvc, ing, cm)
	g, err := (&Cluster{Dynamic: dyn}).Graph(context.Background(), "ns")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]GraphNode{}
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}
	has := func(from, to string) bool {
		for _, e := range g.Edges {
			if e.From == from && e.To == to {
				return true
			}
		}
		return false
	}
	if _, ok := byID["ReplicaSet/web-1"]; ok {
		t.Error("a Deployment's ReplicaSet should be folded into it")
	}
	if d := byID["Deployment/web"]; d.PodsAll != 1 || len(d.Pods) != 1 {
		t.Errorf("pod not folded into its deployment: %+v", d)
	}
	for _, e := range [][2]string{{"Ingress/web", "Service/web"}, {"Service/web", "Deployment/web"},
		{"Deployment/web", "ConfigMap/web-cfg"}, {"Deployment/web", "Secret/web-tls"}, {"Ingress/web", "Service/gone"}} {
		if !has(e[0], e[1]) {
			t.Errorf("missing edge %s -> %s (edges %v)", e[0], e[1], g.Edges)
		}
	}
	if n := byID["Service/gone"]; !n.Missing || n.Health != Failing {
		t.Errorf("an ingress backend that doesn't exist should show as missing: %+v", n)
	}
	if n := byID["Secret/web-tls"]; !n.Missing {
		t.Errorf("a referenced secret that doesn't exist should show as missing: %+v", n)
	}
	if n := byID["Service/ghost"]; n.Health != Warning || n.Status != "Selects nothing" {
		t.Errorf("a service selecting nothing: %+v", n)
	}
	if n := byID["Ingress/web"]; n.Health != Failing {
		t.Errorf("ingress with a missing backend: %+v", n)
	}
}

func TestClusterMap(t *testing.T) {
	q := func(s string) resource.Quantity { return resource.MustParse(s) }
	node := func(name, pool string, ready bool) *corev1.Node {
		st := corev1.ConditionFalse
		if ready {
			st = corev1.ConditionTrue
		}
		return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"karpenter.sh/nodepool": pool, "node.kubernetes.io/instance-type": "m6a.large"}},
			Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: q("2"), corev1.ResourceMemory: q("8Gi")},
				Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: st}}}}
	}
	pod := func(ns, name, node string, phase corev1.PodPhase, ready bool) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns,
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: name[:len(name)-2] + "-abc"}}},
			Spec: corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{Name: "c",
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: q("500m")}}}}},
			Status: corev1.PodStatus{Phase: phase, ContainerStatuses: []corev1.ContainerStatus{{Ready: ready}}}}
	}
	cs := kubefake.NewSimpleClientset(
		node("n1", "apps", true), node("n2", "apps", false), node("n3", "infra", true),
		pod("shop", "web-1", "n1", corev1.PodRunning, true), pod("shop", "web-2", "n2", corev1.PodRunning, false),
		pod("kube-system", "dns-1", "n3", corev1.PodRunning, true))
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		{Version: "v1", Resource: "services"}: "List", {Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}: "List"})
	cm, err := (&Cluster{Typed: cs, Dynamic: dyn}).ClusterMap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cm.Nodes != 3 || cm.Pods != 3 || len(cm.Pools) != 2 {
		t.Fatalf("totals: %+v", cm)
	}
	apps := cm.Pools[0]
	if apps.Name != "apps" || len(apps.Nodes) != 2 || apps.Health != Warning || apps.CPUReq != 1000 || apps.CPUAlloc != 4000 {
		t.Fatalf("apps pool: %+v", apps)
	}
	if cm.Namespaces[0].Name != "shop" || cm.Namespaces[0].Workloads != 1 || cm.Namespaces[0].Warning != 1 || cm.Namespaces[1].System != true {
		t.Fatalf("namespaces (apps first, system last): %+v", cm.Namespaces)
	}
	links := map[string]int{}
	for _, l := range cm.Links {
		links[l.Namespace+">"+l.Pool] = l.Pods
	}
	if links["shop>apps"] != 2 || links["kube-system>infra"] != 1 {
		t.Fatalf("links: %v", links)
	}
}

func TestSplitNamespaces(t *testing.T) {
	for in, want := range map[string]string{"": "", "a": "a", "a,b": "a|b", " a , b ,a,": "a|b"} {
		if got := strings.Join(SplitNamespaces(in), "|"); got != want {
			t.Errorf("SplitNamespaces(%q) = %q, want %q", in, got, want)
		}
	}
}
