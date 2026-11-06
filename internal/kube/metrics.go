package kube

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Live CPU and memory come from metrics-server (metrics.k8s.io), the same source as
// `kubectl top`. Clusters without it simply get no usage columns' values ("-").

var podMetricsGVR = schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}

// Usage is one pod's live consumption.
type Usage struct {
	CPU int64 // millicores
	Mem int64 // bytes
}

// PodUsage returns usage per "namespace/name". An error means metrics are not
// available (no metrics-server, or not allowed); callers show "-".
func (c *Cluster) PodUsage(ctx context.Context, namespace string) (map[string]Usage, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	ri := c.Dynamic.Resource(podMetricsGVR)
	var list *unstructured.UnstructuredList
	var err error
	if namespace != "" {
		list, err = ri.Namespace(namespace).List(ctx, metav1.ListOptions{})
	} else {
		list, err = ri.List(ctx, metav1.ListOptions{})
	}
	if err != nil {
		return nil, err
	}
	out := make(map[string]Usage, len(list.Items))
	for _, it := range list.Items {
		var u Usage
		cs, _, _ := unstructured.NestedSlice(it.Object, "containers")
		for _, x := range cs {
			m, _ := x.(map[string]any)
			usage, _ := m["usage"].(map[string]any)
			if q, err := resource.ParseQuantity(fmt.Sprint(usage["cpu"])); err == nil {
				u.CPU += q.MilliValue()
			}
			if q, err := resource.ParseQuantity(fmt.Sprint(usage["memory"])); err == nil {
				u.Mem += q.Value()
			}
		}
		out[it.GetNamespace()+"/"+it.GetName()] = u
	}
	return out, nil
}

// podResources sums the containers' requests and limits (0 = not set).
func podResources(o map[string]any) (cpuReq, cpuLim, memReq, memLim int64) {
	cs, _ := lookup(o, "spec", "containers").([]any)
	q := func(m map[string]any, kind, res string) int64 {
		v := str(m, "resources", kind, res)
		if v == "" {
			return 0
		}
		p, err := resource.ParseQuantity(v)
		if err != nil {
			return 0
		}
		if res == "cpu" {
			return p.MilliValue()
		}
		return p.Value()
	}
	for _, x := range cs {
		m, _ := x.(map[string]any)
		cpuReq += q(m, "requests", "cpu")
		cpuLim += q(m, "limits", "cpu")
		memReq += q(m, "requests", "memory")
		memLim += q(m, "limits", "memory")
	}
	return
}

// FormatCPU is "250m" below a core, "1.5" above.
func FormatCPU(m int64) string {
	if m < 1000 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%.2g", float64(m)/1000)
}

// FormatMem is Mi below a GiB, Gi above (kubectl top style).
func FormatMem(b int64) string {
	if b >= 1<<30 {
		return fmt.Sprintf("%.1fGi", float64(b)/(1<<30))
	}
	return fmt.Sprintf("%dMi", b>>20)
}

// usagePct is use as a share of the limit, else of the request; -1 when neither is set.
func usagePct(use, req, lim int64) int {
	switch {
	case lim > 0:
		return int(use * 100 / lim)
	case req > 0:
		return int(use * 100 / req)
	}
	return -1
}

// addUsage fills the CPU / MEMORY cells of pod rows: "120m 48%" -- the percentage is
// of the limit (or of the request when there is no limit), so a pod about to be
// throttled or OOM-killed stands out. Rows of a pod near its memory limit turn amber.
func addUsage(rows []Row, objs map[string]map[string]any, use map[string]Usage) {
	for i := range rows {
		r := &rows[i]
		u, ok := use[r.Namespace+"/"+r.Name]
		if !ok {
			r.Cells["CPU"], r.Cells["MEMORY"] = "-", "-"
			continue
		}
		cReq, cLim, mReq, mLim := podResources(objs[r.Namespace+"/"+r.Name])
		r.Cells["CPU"] = withPct(FormatCPU(u.CPU), usagePct(u.CPU, cReq, cLim))
		r.Cells["MEMORY"] = withPct(FormatMem(u.Mem), usagePct(u.Mem, mReq, mLim))
		if mLim > 0 && u.Mem*100/mLim >= 90 && r.Health == Healthy {
			r.Health = Warning
			r.Status = "Running · memory near limit"
		}
	}
}

func withPct(v string, p int) string {
	if p < 0 {
		return v
	}
	return fmt.Sprintf("%s %d%%", v, p)
}
