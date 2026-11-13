package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/jsonpath"
)

// printerColumn is one of a CRD's additionalPrinterColumns, ready to evaluate.
type printerColumn struct {
	Name string // upper-cased, as the table shows it
	Type string // string, integer, number, boolean, date
	path *jsonpath.JSONPath
}

type printerCache struct {
	cols []printerColumn
	at   time.Time
}

var crdGVR = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}

// printerColumns reads the CRD of kind k and returns the columns it declares for the
// version K-Vision lists. Kept for a few minutes per cluster; nil when there are none
// or the CRD can't be read (RBAC), and the table simply has no extra columns.
func (c *Cluster) printerColumns(ctx context.Context, k Kind) []printerColumn {
	id := k.ID()
	c.pcMu.Lock()
	if e, ok := c.pc[id]; ok && time.Since(e.at) < 5*time.Minute {
		c.pcMu.Unlock()
		return e.cols
	}
	c.pcMu.Unlock()

	var cols []printerColumn
	if crd, err := c.Dynamic.Resource(crdGVR).Get(ctx, k.Name+"."+k.Group, metav1.GetOptions{}); err == nil {
		cols = parsePrinterColumns(crd.Object, k.Version)
	} else if ctx.Err() != nil {
		return nil // cancelled: don't cache
	}
	c.pcMu.Lock()
	if c.pc == nil {
		c.pc = map[string]printerCache{}
	}
	c.pc[id] = printerCache{cols: cols, at: time.Now()}
	c.pcMu.Unlock()
	return cols
}

// parsePrinterColumns takes the additionalPrinterColumns of one version of a CRD
// object. Name, namespace and age are columns of their own already, so a column that
// repeats them (most CRDs add "Age" on .metadata.creationTimestamp) is left out.
func parsePrinterColumns(crd map[string]any, version string) []printerColumn {
	vs, _ := lookup(crd, "spec", "versions").([]any)
	var raw []any
	for _, v := range vs {
		m, _ := v.(map[string]any)
		if str(m, "name") == version {
			raw, _ = m["additionalPrinterColumns"].([]any)
		}
	}
	var out []printerColumn
	seen := map[string]bool{"NAME": true, "NAMESPACE": true, "AGE": true}
	for _, x := range raw {
		m, _ := x.(map[string]any)
		name := strings.ToUpper(strings.TrimSpace(str(m, "name")))
		path := strings.TrimSpace(str(m, "jsonPath"))
		if name == "" || path == "" || seen[name] || path == ".metadata.creationTimestamp" {
			continue
		}
		jp := jsonpath.New(name).AllowMissingKeys(true)
		if err := jp.Parse("{" + path + "}"); err != nil {
			continue
		}
		seen[name] = true
		out = append(out, printerColumn{Name: name, Type: str(m, "type"), path: jp})
	}
	return out
}

// addPrinterCells fills a row's cells from the CRD columns.
func addPrinterCells(r *Row, cols []printerColumn, obj map[string]any) {
	for _, pc := range cols {
		v := printerValue(pc, obj)
		if _, ok := r.Cells[pc.Name]; !ok || v != "" {
			r.Cells[pc.Name] = v
		}
	}
}

func printerValue(pc printerColumn, obj map[string]any) string {
	res, err := pc.path.FindResults(obj)
	if err != nil {
		return ""
	}
	var parts []string
	for _, set := range res {
		for _, v := range set {
			if !v.IsValid() || !v.CanInterface() {
				continue
			}
			x := v.Interface()
			switch t := x.(type) {
			case nil:
				continue
			case string:
				if pc.Type == "date" {
					if ts, err := time.Parse(time.RFC3339, t); err == nil {
						parts = append(parts, HumanAge(ts))
						continue
					}
				}
				parts = append(parts, t)
			case map[string]any, []any:
				b, _ := json.Marshal(t)
				parts = append(parts, string(b))
			default:
				parts = append(parts, fmt.Sprint(t))
			}
		}
	}
	return strings.Join(parts, ",")
}
