package kube

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EventsDashboard is the cluster's warnings in one place: every Warning event of the
// last window, grouped by what went wrong (reason + the message with names and
// numbers taken out), so "FailedMount ×42 on the gitlab-runner-spot pool" is one
// line instead of 42 rows of `kubectl get events`.
type EventsDashboard struct {
	Window    string        `json:"window"`
	Warnings  int           `json:"warnings"` // Warning events (counting repeats) in the window
	Objects   int           `json:"objects"`  // distinct objects with a warning
	Groups    []EventGroup  `json:"groups"`   // worst first
	ByReason  []EventCount  `json:"byReason"` // reason -> count
	ByNs      []EventCount  `json:"byNamespace"`
	ByPool    []EventCount  `json:"byPool"`   // node pool of the node involved (pods: the node they run on)
	Timeline  []int         `json:"timeline"` // warnings per bucket, oldest first
	BucketMin int           `json:"bucketMinutes"`
	Recent    []EventRecent `json:"recent"` // newest warnings, one per object+reason
	Errors    []string      `json:"errors,omitempty"`
}

type EventGroup struct {
	Reason     string     `json:"reason"`
	Kind       string     `json:"kind"`    // involved object kind
	Message    string     `json:"message"` // a real example message
	Pattern    string     `json:"pattern"` // the message with names/numbers masked: what groups it
	Count      int        `json:"count"`   // events, counting repeats
	Objects    int        `json:"objects"`
	Namespaces []string   `json:"namespaces"`
	Pools      []string   `json:"pools,omitempty"`
	Examples   []EventObj `json:"examples"` // up to 5 objects
	First      time.Time  `json:"first"`
	Last       time.Time  `json:"last"`
	Level      Health     `json:"level"`
	Hint       string     `json:"hint,omitempty"`
}

type EventObj struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type EventCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type EventRecent struct {
	EventObj
	Reason  string    `json:"reason"`
	Message string    `json:"message"`
	Count   int       `json:"count"`
	Last    time.Time `json:"last"`
	Level   Health    `json:"level"`
}

// Reasons that mean something is broken now, not just slow. Everything else is amber.
var badReasons = map[string]bool{
	"BackOff": true, "Failed": true, "FailedMount": true, "FailedAttachVolume": true, "FailedCreatePodSandBox": true,
	"OOMKilling": true, "Evicted": true, "FailedCreate": true, "FailedKillPod": true, "NodeNotReady": true,
	"ErrImagePull": true, "InspectFailed": true, "FailedSync": true, "Unhealthy": true, "FailedPostStartHook": true,
	"FailedPreStopHook": true, "NetworkNotReady": true, "SystemOOM": true, "FailedBinding": true, "ProvisioningFailed": true,
}

// What to look at, for the reasons that come up most.
var eventHints = map[string]string{
	"FailedScheduling":        "No node fits: see the message (insufficient cpu/memory, taints, node selector). With Karpenter, check the NodePool limits and NodeClaims.",
	"FailedMount":             "A volume could not be mounted. \"driver … not found\" = its CSI driver isn't running on that node yet (often on brand-new nodes).",
	"FailedAttachVolume":      "The volume is still attached elsewhere or in another zone.",
	"BackOff":                 "A container keeps crashing (CrashLoopBackOff) or its image can't be pulled. Open the pod's logs (previous container).",
	"Unhealthy":               "A liveness/readiness/startup probe fails. Check the probe path/port and how long the app takes to start.",
	"Failed":                  "Usually an image pull: wrong tag, missing registry credentials, or the registry is unreachable.",
	"OOMKilling":              "The kernel killed a process for memory: raise the memory limit or find the leak.",
	"Evicted":                 "The node ran short of memory or disk and evicted pods.",
	"FailedCreatePodSandBox":  "The pod network (CNI) could not set up the pod: often out of IPs on the node/subnet.",
	"NodeNotReady":            "A node stopped reporting. Spot interruption, kubelet trouble or the node is gone.",
	"FailedCreate":            "A controller couldn't create pods: quota, admission webhook or PodSecurity refused it.",
	"ProvisioningFailed":      "A PVC could not get a volume: check the StorageClass and the CSI driver's logs.",
	"FailedGetResourceMetric": "The HPA can't read metrics: metrics-server down or the pods have no resource requests.",
}

var (
	reUUID = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	reIP   = regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}(:\d+)?\b`)
	reNum  = regexp.MustCompile(`\b\d+(\.\d+)?\b`)
	reQuot = regexp.MustCompile(`"[^"]*"`)
	rePod  = regexp.MustCompile(`\b[a-z0-9]([-a-z0-9]*[a-z0-9])?-[a-z0-9]{5,10}(-[a-z0-9]{5})?\b`)
	// kubelet's "pod web-7d9f8-abcde_shop(<uid>)" reference
	rePodRef = regexp.MustCompile(`[a-z0-9][-a-z0-9.]*_[-a-z0-9]+\(…\)`)
	reNode   = regexp.MustCompile(`\bip-\d+-\d+-\d+-\d+[\w.-]*`)
)

// eventPattern masks what differs between two events of the same problem: names in
// quotes, pod/node names, IPs, UUIDs and numbers. "0/34 nodes are available: 2
// Insufficient cpu" and "0/36 nodes … 3 Insufficient cpu" become one pattern.
func eventPattern(msg string) string {
	s := firstLine(msg)
	if len(s) > 400 {
		s = s[:400]
	}
	s = reUUID.ReplaceAllString(s, "…")
	s = reQuot.ReplaceAllString(s, `"…"`)
	s = rePodRef.ReplaceAllString(s, "<pod>")
	s = reNode.ReplaceAllString(s, "<node>")
	s = reIP.ReplaceAllString(s, "<ip>")
	s = rePod.ReplaceAllString(s, "<name>")
	s = reNum.ReplaceAllString(s, "N")
	return strings.Join(strings.Fields(s), " ")
}

func eventCount(e corev1.Event) int {
	if e.Series != nil && int(e.Series.Count) > 0 {
		return int(e.Series.Count)
	}
	if e.Count > 0 {
		return int(e.Count)
	}
	return 1
}

func eventFirst(e corev1.Event) time.Time {
	if !e.FirstTimestamp.IsZero() {
		return e.FirstTimestamp.Time
	}
	return eventTime(e)
}

// lastSeen: the newest of the event's own times (Series.LastObservedTime for new-style events).
func lastSeen(e corev1.Event) time.Time {
	t := eventTime(e)
	if e.Series != nil && e.Series.LastObservedTime.After(t) {
		t = e.Series.LastObservedTime.Time
	}
	return t
}

// Events builds the dashboard from the Warning events of the last `window` (default
// 1h, max 24h -- the API server keeps events about an hour by default anyway) in
// the given namespaces ("" = all).
func (c *Cluster) Events(ctx context.Context, namespace string, window time.Duration) (*EventsDashboard, error) {
	if window <= 0 {
		window = time.Hour
	}
	if window > 24*time.Hour {
		window = 24 * time.Hour
	}
	var (
		evs   []corev1.Event
		nodes []corev1.Node
		pods  []corev1.Pod
		errs  []string
		mu    sync.Mutex
		wg    sync.WaitGroup
	)
	fail := func(what string, err error) {
		mu.Lock()
		errs = append(errs, what+": "+firstLine(err.Error()))
		mu.Unlock()
	}
	nss := SplitNamespaces(namespace)
	if len(nss) == 0 {
		nss = []string{""}
	}
	opts := metav1.ListOptions{FieldSelector: "type=Warning"}
	for _, ns := range nss {
		wg.Add(1)
		go func(ns string) {
			defer wg.Done()
			l, err := c.Typed.CoreV1().Events(ns).List(ctx, opts)
			if err != nil {
				fail("events", err)
				return
			}
			mu.Lock()
			evs = append(evs, l.Items...)
			mu.Unlock()
		}(ns)
	}
	// Nodes and pods only to name the node pool an event belongs to; missing RBAC on
	// them just leaves the pool column empty.
	wg.Add(2)
	go func() {
		defer wg.Done()
		if l, err := c.Typed.CoreV1().Nodes().List(ctx, metav1.ListOptions{}); err == nil {
			nodes = l.Items
		}
	}()
	go func() {
		defer wg.Done()
		if l, err := c.Typed.CoreV1().Pods("").List(ctx, metav1.ListOptions{ResourceVersion: "0"}); err == nil {
			pods = l.Items
		}
	}()
	wg.Wait()
	if len(evs) == 0 && len(errs) > 0 {
		return nil, errors.New(errs[0])
	}
	pool := map[string]string{} // node name -> pool
	for _, n := range nodes {
		p, _ := poolOf(n.Labels)
		pool[n.Name] = p
	}
	podNode := map[string]string{} // ns/name -> node
	for _, p := range pods {
		podNode[p.Namespace+"/"+p.Name] = p.Spec.NodeName
	}
	return buildEvents(evs, pool, podNode, window, time.Now(), errs), nil
}

// buildEvents is the pure part of Events, so it can be tested without a cluster.
func buildEvents(evs []corev1.Event, pool, podNode map[string]string, window time.Duration, now time.Time, errs []string) *EventsDashboard {
	since := now.Add(-window)
	bucket := 5 * time.Minute
	switch {
	case window > 6*time.Hour:
		bucket = time.Hour
	case window > time.Hour:
		bucket = 15 * time.Minute
	}
	nb := int(window / bucket)
	d := &EventsDashboard{Window: window.String(), Groups: []EventGroup{}, ByReason: []EventCount{}, ByNs: []EventCount{},
		ByPool: []EventCount{}, Timeline: make([]int, nb), BucketMin: int(bucket / time.Minute), Recent: []EventRecent{}, Errors: errs}

	poolOfEvent := func(e corev1.Event) string {
		o := e.InvolvedObject
		switch o.Kind {
		case "Node":
			return pool[o.Name]
		case "Pod":
			if n := podNode[o.Namespace+"/"+o.Name]; n != "" {
				return pool[n]
			}
		}
		if e.Source.Host != "" {
			return pool[e.Source.Host]
		}
		return ""
	}

	type gkey struct{ reason, kind, pattern string }
	groups := map[gkey]*EventGroup{}
	gobjs := map[gkey]map[EventObj]bool{}
	gns := map[gkey]map[string]bool{}
	gpools := map[gkey]map[string]bool{}
	reasons, nsCount, poolCount := map[string]int{}, map[string]int{}, map[string]int{}
	allObjs := map[EventObj]bool{}
	recent := map[string]*EventRecent{}

	for _, e := range evs {
		last := lastSeen(e)
		if last.Before(since) {
			continue
		}
		n := eventCount(e)
		// A long-running series started before the window: count what fits, roughly.
		if first := eventFirst(e); first.Before(since) && n > 1 && last.After(first) {
			share := float64(last.Sub(since)) / float64(last.Sub(first))
			if m := int(float64(n)*share + 0.5); m >= 1 {
				n = m
			} else {
				n = 1
			}
		}
		o := EventObj{Kind: e.InvolvedObject.Kind, Namespace: e.InvolvedObject.Namespace, Name: e.InvolvedObject.Name}
		reason := e.Reason
		if reason == "" {
			reason = "Unknown"
		}
		k := gkey{reason, o.Kind, eventPattern(e.Message)}
		g := groups[k]
		if g == nil {
			lvl := Warning
			if badReasons[reason] {
				lvl = Failing
			}
			g = &EventGroup{Reason: reason, Kind: o.Kind, Message: firstLine(e.Message), Pattern: k.pattern, First: eventFirst(e), Last: last,
				Level: lvl, Hint: eventHints[reason], Examples: []EventObj{}}
			groups[k], gobjs[k], gns[k], gpools[k] = g, map[EventObj]bool{}, map[string]bool{}, map[string]bool{}
		}
		g.Count += n
		if !gobjs[k][o] {
			gobjs[k][o] = true
			if len(g.Examples) < 5 {
				g.Examples = append(g.Examples, o)
			}
		}
		if f := eventFirst(e); f.Before(g.First) {
			g.First = f
		}
		if last.After(g.Last) {
			g.Last, g.Message = last, firstLine(e.Message)
		}
		ns := o.Namespace
		if ns == "" {
			ns = "(cluster)"
		}
		gns[k][ns] = true
		p := poolOfEvent(e)
		if p != "" {
			gpools[k][p] = true
			poolCount[p] += n
		}
		reasons[reason] += n
		nsCount[ns] += n
		allObjs[o] = true
		d.Warnings += n
		// A repeating event is n occurrences between first and last: spread them over
		// that span, or one event that fired 250 times all day is one tall bar "now".
		spreadOver(d.Timeline, since, bucket, eventFirst(e), last, n)
		rk := o.Kind + "/" + o.Namespace + "/" + o.Name + "/" + reason
		if r := recent[rk]; r == nil || last.After(r.Last) {
			cnt := n
			if r != nil {
				cnt += r.Count
			}
			recent[rk] = &EventRecent{EventObj: o, Reason: reason, Message: firstLine(e.Message), Count: cnt, Last: last, Level: g.Level}
		} else {
			r.Count += n
		}
	}
	d.Objects = len(allObjs)
	keys := func(m map[string]bool) []string {
		out := make([]string, 0, len(m))
		for s := range m {
			out = append(out, s)
		}
		sort.Strings(out)
		return out
	}
	for k, g := range groups {
		g.Objects = len(gobjs[k])
		g.Namespaces = keys(gns[k])
		g.Pools = keys(gpools[k])
		d.Groups = append(d.Groups, *g)
	}
	// Broken before slow, then by how many objects it hits, then by volume.
	sort.Slice(d.Groups, func(i, j int) bool {
		a, b := d.Groups[i], d.Groups[j]
		if (a.Level == Failing) != (b.Level == Failing) {
			return a.Level == Failing
		}
		if a.Objects != b.Objects {
			return a.Objects > b.Objects
		}
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.Last.After(b.Last)
	})
	counts := func(m map[string]int) []EventCount {
		out := make([]EventCount, 0, len(m))
		for k, v := range m {
			out = append(out, EventCount{k, v})
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Count != out[j].Count {
				return out[i].Count > out[j].Count
			}
			return out[i].Name < out[j].Name
		})
		return out
	}
	d.ByReason, d.ByNs, d.ByPool = counts(reasons), counts(nsCount), counts(poolCount)
	for _, r := range recent {
		d.Recent = append(d.Recent, *r)
	}
	sort.Slice(d.Recent, func(i, j int) bool { return d.Recent[i].Last.After(d.Recent[j].Last) })
	if len(d.Recent) > 50 {
		d.Recent = d.Recent[:50]
	}
	return d
}

// spreadOver adds n occurrences, evenly between from and to, into per-bucket counts
// starting at since. Times before since are clipped (n was already scaled to the window).
func spreadOver(tl []int, since time.Time, bucket time.Duration, from, to time.Time, n int) {
	nb := len(tl)
	if nb == 0 || n <= 0 {
		return
	}
	idx := func(t time.Time) int {
		i := int(t.Sub(since) / bucket)
		if i < 0 {
			return 0
		}
		if i >= nb {
			return nb - 1
		}
		return i
	}
	if from.Before(since) {
		from = since
	}
	a, b := idx(from), idx(to)
	if b < a {
		a = b
	}
	span := b - a + 1
	for k := 0; k < span; k++ {
		share := n / span
		if span-1-k < n%span { // the remainder goes to the newest buckets
			share++
		}
		tl[a+k] += share
	}
}
