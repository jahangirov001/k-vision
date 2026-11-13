package kube

import (
	"testing"
	"time"
)

func TestPrinterColumns(t *testing.T) {
	crd := map[string]any{"spec": map[string]any{"versions": []any{
		map[string]any{"name": "v1alpha1", "additionalPrinterColumns": []any{
			map[string]any{"name": "Old", "jsonPath": ".spec.old"},
		}},
		map[string]any{"name": "v1", "additionalPrinterColumns": []any{
			map[string]any{"name": "Host", "type": "string", "jsonPath": ".spec.host"},
			map[string]any{"name": "Suspend", "type": "boolean", "jsonPath": ".spec.suspend"},
			map[string]any{"name": "Progress", "type": "integer", "jsonPath": ".status.progress"},
			map[string]any{"name": "Last Success", "type": "date", "jsonPath": ".status.lastSuccess"},
			map[string]any{"name": "Ports", "type": "string", "jsonPath": ".spec.ports[*].port"},
			map[string]any{"name": "Missing", "type": "string", "jsonPath": ".status.nope"},
			map[string]any{"name": "Age", "type": "date", "jsonPath": ".metadata.creationTimestamp"},
			map[string]any{"name": "Bad", "jsonPath": ".spec[["},
		}},
	}}}
	cols := parsePrinterColumns(crd, "v1")
	var names []string
	for _, c := range cols {
		names = append(names, c.Name)
	}
	if got := join(names); got != "HOST,SUSPEND,PROGRESS,LAST SUCCESS,PORTS,MISSING" {
		t.Fatalf("columns = %s", got)
	}
	obj := map[string]any{
		"spec":   map[string]any{"host": "db-internal", "suspend": false, "ports": []any{map[string]any{"port": int64(80)}, map[string]any{"port": int64(443)}}},
		"status": map[string]any{"progress": int64(55), "lastSuccess": time.Now().Add(-49 * time.Hour).UTC().Format(time.RFC3339)},
	}
	r := Row{Cells: map[string]string{}}
	addPrinterCells(&r, cols, obj)
	want := map[string]string{"HOST": "db-internal", "SUSPEND": "false", "PROGRESS": "55", "LAST SUCCESS": "2d", "PORTS": "80,443", "MISSING": ""}
	for k, v := range want {
		if r.Cells[k] != v {
			t.Errorf("%s = %q, want %q", k, r.Cells[k], v)
		}
	}
}

func join(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += ","
		}
		out += x
	}
	return out
}
