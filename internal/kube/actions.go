package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"
)

// Every write goes through here, and every one is recorded in the audit log whether
// it worked or not. A console that can delete a prod deployment has to be able to say
// afterwards who did it, from where, and what the object looked like before.

// Audit is one line of ~/.k-vision/audit.log (JSON lines).
type Audit struct {
	Time      time.Time `json:"time"`
	User      string    `json:"user"`
	Context   string    `json:"context"`
	Action    string    `json:"action"`
	Kind      string    `json:"kind"`
	Namespace string    `json:"namespace,omitempty"`
	Name      string    `json:"name"`
	Detail    string    `json:"detail,omitempty"`
	Error     string    `json:"error,omitempty"`
}

var auditMu sync.Mutex

// AuditPath is where the log lives; KV_AUDIT_LOG overrides it.
func AuditPath() string {
	if p := os.Getenv("KV_AUDIT_LOG"); p != "" {
		return p
	}
	return filepath.Join(DataDir(), "audit.log")
}

// DataDir is ~/.k-vision: settings, audit log, window profile, token.
// The first run after the rename from Kube Console moves ~/.kube-console here, so
// nobody loses their audit log or removed-cluster list.
func DataDir() string {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".k-vision")
	migrateOnce.Do(func() {
		old := filepath.Join(home, ".kube-console")
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			if _, err := os.Stat(old); err == nil {
				if os.Rename(old, dir) == nil {
					_ = os.Symlink(dir, old) // older scripts and the old app still find it
				}
			}
		}
	})
	return dir
}

var migrateOnce sync.Once

func record(a Audit, err error) {
	a.Time = time.Now().UTC()
	a.User = os.Getenv("USER")
	if err != nil {
		a.Error = err.Error()
	}
	line, _ := json.Marshal(a)
	auditMu.Lock()
	defer auditMu.Unlock()
	p := AuditPath()
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	f, ferr := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if ferr != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// Delete removes one object.
func (c *Cluster) Delete(ctx context.Context, k Kind, namespace, name string) (err error) {
	a := Audit{Context: c.Context, Action: "delete", Kind: k.ID(), Namespace: namespace, Name: name}
	defer func() { record(a, err) }()
	policy := metav1.DeletePropagationBackground
	opts := metav1.DeleteOptions{PropagationPolicy: &policy}
	ri := c.Dynamic.Resource(k.GVR())
	if k.Namespaced {
		return ri.Namespace(namespace).Delete(ctx, name, opts)
	}
	return ri.Delete(ctx, name, opts)
}

// Scale sets replicas through the scale subresource, which works for Deployments,
// StatefulSets, ReplicaSets and any CRD that implements it.
func (c *Cluster) Scale(ctx context.Context, k Kind, namespace, name string, replicas int32) (err error) {
	a := Audit{Context: c.Context, Action: "scale", Kind: k.ID(), Namespace: namespace, Name: name,
		Detail: fmt.Sprintf("replicas=%d", replicas)}
	defer func() { record(a, err) }()
	patch := []byte(fmt.Sprintf(`{"spec":{"replicas":%d}}`, replicas))
	_, err = c.Dynamic.Resource(k.GVR()).Namespace(namespace).
		Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}, "scale")
	return err
}

// Restart is `kubectl rollout restart`: bump an annotation on the pod template.
func (c *Cluster) Restart(ctx context.Context, k Kind, namespace, name string) (err error) {
	a := Audit{Context: c.Context, Action: "restart", Kind: k.ID(), Namespace: namespace, Name: name}
	defer func() { record(a, err) }()
	switch k.ID() {
	case "deployments.apps", "statefulsets.apps", "daemonsets.apps":
	default:
		return fmt.Errorf("%s cannot be restarted; only deployments, statefulsets and daemonsets", k.ID())
	}
	patch := []byte(fmt.Sprintf(
		`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":%q}}}}}`,
		time.Now().Format(time.RFC3339)))
	_, err = c.Dynamic.Resource(k.GVR()).Namespace(namespace).
		Patch(ctx, name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	return err
}

// YAML returns an object as editable YAML, without the server-managed noise.
func (c *Cluster) YAML(ctx context.Context, k Kind, namespace, name string) (string, error) {
	u, err := c.Get(ctx, k, namespace, name)
	if err != nil {
		return "", err
	}
	unstructured.RemoveNestedField(u.Object, "metadata", "managedFields")
	data, err := yaml.Marshal(u.Object)
	return string(data), err
}

// Apply replaces an object with edited YAML. The resourceVersion in the YAML makes it
// a compare-and-swap: if someone changed the object since it was opened, the update
// is refused instead of silently overwriting their change.
func (c *Cluster) Apply(ctx context.Context, k Kind, namespace, name, text string) (err error) {
	a := Audit{Context: c.Context, Action: "edit", Kind: k.ID(), Namespace: namespace, Name: name}
	defer func() { record(a, err) }()
	var obj map[string]any
	if err = yaml.Unmarshal([]byte(text), &obj); err != nil {
		return fmt.Errorf("invalid YAML: %w", err)
	}
	u := &unstructured.Unstructured{Object: obj}
	if u.GetName() != name || (k.Namespaced && u.GetNamespace() != namespace) {
		return fmt.Errorf("name/namespace changed in the YAML (%s/%s); edit cannot rename an object",
			u.GetNamespace(), u.GetName())
	}
	ri := c.Dynamic.Resource(k.GVR())
	if k.Namespaced {
		_, err = ri.Namespace(namespace).Update(ctx, u, metav1.UpdateOptions{})
	} else {
		_, err = ri.Update(ctx, u, metav1.UpdateOptions{})
	}
	return err
}

// Cordon marks a node (un)schedulable.
func (c *Cluster) Cordon(ctx context.Context, node string, on bool) (err error) {
	action := "cordon"
	if !on {
		action = "uncordon"
	}
	a := Audit{Context: c.Context, Action: action, Kind: "nodes", Name: node}
	defer func() { record(a, err) }()
	patch := []byte(fmt.Sprintf(`{"spec":{"unschedulable":%t}}`, on))
	_, err = c.Typed.CoreV1().Nodes().Patch(ctx, node, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}

// Drain cordons a node and evicts its pods through the Eviction API, so
// PodDisruptionBudgets are respected. DaemonSet pods and mirror pods are left, as
// kubectl drain --ignore-daemonsets does. Returns the pods it could not evict.
func (c *Cluster) Drain(ctx context.Context, node string) (failed []string, err error) {
	a := Audit{Context: c.Context, Action: "drain", Kind: "nodes", Name: node}
	defer func() {
		if len(failed) > 0 {
			a.Detail = fmt.Sprintf("not evicted: %v", failed)
		}
		record(a, err)
	}()
	if err = c.Cordon(ctx, node, true); err != nil {
		return nil, err
	}
	pods, err := c.Typed.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + node})
	if err != nil {
		return nil, err
	}
	for _, p := range pods.Items {
		if _, mirror := p.Annotations["kubernetes.io/config.mirror"]; mirror {
			continue
		}
		owned := false
		for _, o := range p.OwnerReferences {
			if o.Kind == "DaemonSet" {
				owned = true
			}
		}
		if owned {
			continue
		}
		ev := &policyv1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: p.Name, Namespace: p.Namespace}}
		if e := c.Typed.CoreV1().Pods(p.Namespace).EvictV1(ctx, ev); e != nil {
			failed = append(failed, p.Namespace+"/"+p.Name+": "+e.Error())
		}
	}
	return failed, nil
}
