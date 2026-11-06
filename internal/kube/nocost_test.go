package kube

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// A cluster without OpenCost: Cost reports ErrNoCost (the page shows the install
// hint) instead of failing, and nothing is created in the cluster.
func TestCostWithoutOpenCost(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}, Selector: map[string]string{"app": "web"}}})
	c := &Cluster{Context: "no-opencost", Typed: cs}
	_, err := c.Cost(context.Background(), "7d", "namespace", "", false)
	if !errors.Is(err, ErrNoCost) {
		t.Fatalf("want ErrNoCost, got %v", err)
	}
	for _, a := range cs.Actions() {
		if a.GetVerb() != "list" {
			t.Errorf("unexpected %s %s: only reads allowed", a.GetVerb(), a.GetResource().Resource)
		}
	}
}
