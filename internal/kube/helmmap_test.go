package kube

import "testing"

func TestHelmMapHelpers(t *testing.T) {
	if groupOf("apps/v1") != "apps" || groupOf("v1") != "" || groupOf("networking.k8s.io/v1") != "networking.k8s.io" {
		t.Error("groupOf")
	}
	if !(kindRank("Ingress") < kindRank("Service") && kindRank("Service") < kindRank("Deployment") &&
		kindRank("Deployment") < kindRank("ConfigMap") && kindRank("ConfigMap") < kindRank("ClusterRole")) {
		t.Error("kindRank order")
	}
	objs, err := parseManifests("---\n# Source: x/templates/svc.yaml\napiVersion: v1\nkind: Service\nmetadata:\n  name: a\n---\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: a\n  namespace: other\n")
	if err != nil || len(objs) != 2 || objs[1].GetNamespace() != "other" {
		t.Fatalf("manifest parse: %v %d", err, len(objs))
	}
}

func TestUsageCells(t *testing.T) {
	rows := []Row{{Namespace: "a", Name: "p", Health: Healthy, Status: "Running", Cells: map[string]string{}},
		{Namespace: "a", Name: "q", Health: Healthy, Cells: map[string]string{}}}
	objs := map[string]map[string]any{"a/p": {"spec": map[string]any{"containers": []any{
		map[string]any{"resources": map[string]any{"requests": map[string]any{"cpu": "100m"}, "limits": map[string]any{"memory": "100Mi"}}}}}}}
	addUsage(rows, objs, map[string]Usage{"a/p": {CPU: 50, Mem: 95 << 20}})
	if rows[0].Cells["CPU"] != "50m 50%" || rows[0].Cells["MEMORY"] != "95Mi 95%" {
		t.Errorf("cells %v", rows[0].Cells)
	}
	if rows[0].Health != Warning {
		t.Error("95% of the memory limit should warn")
	}
	if rows[1].Cells["CPU"] != "-" {
		t.Error("no metrics -> -")
	}
	if FormatCPU(1500) != "1.5" || FormatMem(3<<29) != "1.5Gi" {
		t.Error(FormatCPU(1500), FormatMem(3<<29))
	}
}
