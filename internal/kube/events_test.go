package kube

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestEventPattern(t *testing.T) {
	// Real-world messages: the same problem with different numbers
	// and pod names must group together.
	a := eventPattern("0/34 nodes are available: 2 Insufficient cpu, 2 Insufficient memory, 32 node(s) had untolerated taint(s).")
	b := eventPattern("0/36 nodes are available: 3 Insufficient cpu, 3 Insufficient memory, 33 node(s) had untolerated taint(s).")
	if a != b {
		t.Fatalf("scheduling messages should share a pattern:\n%s\n%s", a, b)
	}
	m1 := eventPattern(`MountVolume.MountDevice failed for volume "pvc-aeb2c9a2-e307-4a8c-ab3a-dcd5fa7f7b37" : kubernetes.io/csi: driver name fsx.openzfs.csi.aws.com not found`)
	m2 := eventPattern(`MountVolume.MountDevice failed for volume "pvc-11111111-2222-3333-4444-555555555555" : kubernetes.io/csi: driver name fsx.openzfs.csi.aws.com not found`)
	if m1 != m2 {
		t.Fatalf("mount messages should share a pattern:\n%s\n%s", m1, m2)
	}
	c1 := eventPattern("Back-off restarting failed container app in pod web-7d9f8-z29q9_redcore(6f1c2e3a-1b2c-4d5e-8f90-123456789abc)")
	c2 := eventPattern("Back-off restarting failed container app in pod web-7d9f8-q6ns2_redcore(0a1b2c3d-1b2c-4d5e-8f90-123456789abc)")
	if c1 != c2 {
		t.Fatalf("crash loops of two pods should share a pattern:\n%s\n%s", c1, c2)
	}
	if eventPattern("Back-off restarting failed container") == eventPattern("Readiness probe failed: HTTP probe failed with statuscode: 503") {
		t.Fatal("different problems must not share a pattern")
	}
}

func TestBuildEvents(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	ev := func(ns, pod, reason, msg string, count int32, ago time.Duration) corev1.Event {
		return corev1.Event{
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: ns, Name: pod},
			Reason:         reason, Message: msg, Type: "Warning", Count: count,
			FirstTimestamp: metav1.NewTime(now.Add(-ago)), LastTimestamp: metav1.NewTime(now.Add(-ago)),
		}
	}
	evs := []corev1.Event{
		ev("ci", "runner-a-1", "FailedScheduling", "0/34 nodes are available: 2 Insufficient cpu.", 3, 10*time.Minute),
		ev("ci", "runner-b-2", "FailedScheduling", "0/36 nodes are available: 3 Insufficient cpu.", 2, 5*time.Minute),
		ev("ci", "runner-b-2", "FailedMount", `MountVolume failed for volume "pvc-x" : driver name fsx not found`, 1, 4*time.Minute),
		ev("shop", "api-7d9f8-abcde", "BackOff", "Back-off restarting failed container", 7, 2*time.Minute),
		ev("old", "gone", "BackOff", "Back-off restarting failed container", 1, 3*time.Hour), // outside the window
	}
	pool := map[string]string{"ip-1": "gitlab-runner-spot", "ip-2": "default"}
	podNode := map[string]string{"ci/runner-a-1": "ip-1", "ci/runner-b-2": "ip-1", "shop/api-7d9f8-abcde": "ip-2"}
	d := buildEvents(evs, pool, podNode, time.Hour, now, nil)

	if d.Warnings != 13 || d.Objects != 3 {
		t.Fatalf("warnings=%d objects=%d, want 13 and 3", d.Warnings, d.Objects)
	}
	if len(d.Groups) != 3 {
		t.Fatalf("groups=%d, want 3 (scheduling grouped, mount, backoff): %+v", len(d.Groups), d.Groups)
	}
	// Broken (red) first; FailedScheduling is amber and hits 2 pods.
	if d.Groups[0].Level != Failing || d.Groups[len(d.Groups)-1].Reason != "FailedScheduling" {
		t.Fatalf("order: %s %s %s", d.Groups[0].Reason, d.Groups[1].Reason, d.Groups[2].Reason)
	}
	sched := d.Groups[2]
	if sched.Count != 5 || sched.Objects != 2 || len(sched.Pools) != 1 || sched.Pools[0] != "gitlab-runner-spot" || sched.Hint == "" {
		t.Fatalf("scheduling group: %+v", sched)
	}
	if len(d.ByPool) != 2 || d.ByPool[0].Name != "default" || d.ByPool[0].Count != 7 || d.ByPool[1].Count != 6 {
		t.Fatalf("byPool: %+v", d.ByPool)
	}
	sum := 0
	for _, n := range d.Timeline {
		sum += n
	}
	if len(d.Timeline) != 12 || sum != 13 {
		t.Fatalf("timeline len=%d sum=%d", len(d.Timeline), sum)
	}
	if d.Recent[0].Reason != "BackOff" {
		t.Fatalf("recent newest first: %+v", d.Recent[0])
	}
}

func TestSpreadOver(t *testing.T) {
	since := time.Date(2026, 11, 12, 11, 0, 0, 0, time.UTC)
	tl := make([]int, 12) // 1h in 5-minute buckets
	// 250 occurrences from before the window until now: spread over all 12 buckets.
	spreadOver(tl, since, 5*time.Minute, since.Add(-2*time.Hour), since.Add(59*time.Minute), 250)
	sum := 0
	for _, n := range tl {
		sum += n
		if n < 20 || n > 21 {
			t.Fatalf("uneven spread: %v", tl)
		}
	}
	if sum != 250 || tl[11] != 21 {
		t.Fatalf("sum=%d tl=%v", sum, tl)
	}
	one := make([]int, 12)
	spreadOver(one, since, 5*time.Minute, since.Add(30*time.Minute), since.Add(30*time.Minute), 1)
	if one[6] != 1 {
		t.Fatalf("single event in the wrong bucket: %v", one)
	}
}
