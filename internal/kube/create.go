package kube

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

// CreateResult is what happened to one document of the YAML.
type CreateResult struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
}

// CreateFromYAML creates every object in `text` (one or more YAML or JSON documents,
// separated by ---), like `kubectl create -f`. Objects without a namespace go into
// defaultNS when their kind is namespaced. dryRun asks the API server to validate
// everything -- schema, admission, quotas -- without storing anything, which is what
// the editor's "Validate" button does. Every document is attempted; the results say
// which ones worked. Real creates are audited.
func (c *Cluster) CreateFromYAML(ctx context.Context, text, defaultNS string, dryRun bool) ([]CreateResult, error) {
	objs, err := parseManifests(text)
	if err != nil {
		return nil, err
	}
	if len(objs) == 0 {
		return nil, errors.New("the YAML has no objects in it")
	}
	if len(objs) > 50 {
		return nil, fmt.Errorf("%d objects; create at most 50 at a time", len(objs))
	}
	opts := metav1.CreateOptions{FieldManager: "k-vision"}
	if dryRun {
		opts.DryRun = []string{metav1.DryRunAll}
	}
	out := make([]CreateResult, 0, len(objs))
	for _, u := range objs {
		r := CreateResult{Kind: u.GetKind(), Name: u.GetName(), Namespace: u.GetNamespace()}
		if r.Name == "" && u.GetGenerateName() != "" {
			r.Name = u.GetGenerateName() + "…"
		}
		err := c.createOne(ctx, u, defaultNS, opts, &r)
		if err != nil {
			r.Error = err.Error()
		} else {
			r.OK = true
		}
		if !dryRun {
			kind := strings.ToLower(u.GetKind())
			record(Audit{Context: c.Context, Action: "create", Kind: kind, Namespace: r.Namespace, Name: r.Name}, err)
		}
		out = append(out, r)
	}
	return out, nil
}

func (c *Cluster) createOne(ctx context.Context, u *unstructured.Unstructured, defaultNS string, opts metav1.CreateOptions, r *CreateResult) error {
	gvk := u.GroupVersionKind()
	if gvk.Kind == "" || gvk.Version == "" {
		return errors.New("missing apiVersion or kind")
	}
	m, err := c.MappingFor(gvk)
	if err != nil {
		return fmt.Errorf("this cluster has no %s %s: %w", gvk.GroupVersion(), gvk.Kind, err)
	}
	ri := c.Dynamic.Resource(m.Resource)
	if m.Scope.Name() == meta.RESTScopeNameNamespace {
		if u.GetNamespace() == "" {
			if defaultNS == "" {
				defaultNS = "default"
			}
			u.SetNamespace(defaultNS)
		}
		r.Namespace = u.GetNamespace()
		created, err := ri.Namespace(u.GetNamespace()).Create(ctx, u, opts)
		if err == nil && created.GetName() != "" {
			r.Name = created.GetName()
		}
		return err
	}
	u.SetNamespace("")
	r.Namespace = ""
	created, err := ri.Create(ctx, u, opts)
	if err == nil && created.GetName() != "" {
		r.Name = created.GetName()
	}
	return err
}

// parseManifests splits multi-document YAML (or JSON) into objects; `kind: List`
// is unpacked, empty documents are skipped.
func parseManifests(text string) ([]*unstructured.Unstructured, error) {
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader([]byte(text)), 4096)
	var out []*unstructured.Unstructured
	for i := 1; ; i++ {
		var obj map[string]any
		if err := dec.Decode(&obj); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("document %d is not valid YAML: %w", i, err)
		}
		if len(obj) == 0 {
			continue
		}
		u := &unstructured.Unstructured{Object: obj}
		if u.IsList() {
			list, err := u.ToList()
			if err != nil {
				return nil, fmt.Errorf("document %d: %w", i, err)
			}
			for j := range list.Items {
				out = append(out, &list.Items[j])
			}
			continue
		}
		out = append(out, u)
	}
	return out, nil
}
