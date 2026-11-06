package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// Cost comes from OpenCost (or Kubecost, which serves the same API) when the
// cluster runs it. K-Vision finds the service, opens a private port-forward to
// one of its pods (the API server's service proxy is too slow for OpenCost's
// queries), and asks /allocation/compute. Nothing is installed or changed; with no
// OpenCost the Cost view says so and how to add it.
//
// The figures are OpenCost's: on-demand list prices of the nodes (or your AWS
// billing data when OpenCost has it), split across pods by what they request.
// "Idle" is what you pay for and nobody requested.

type CostRow struct {
	Name       string  `json:"name"`
	Total      float64 `json:"total"`
	CPU        float64 `json:"cpu"`
	RAM        float64 `json:"ram"`
	PV         float64 `json:"pv"`
	Network    float64 `json:"network"`
	LB         float64 `json:"lb"`
	GPU        float64 `json:"gpu"`
	Efficiency float64 `json:"efficiency"` // usage / request, 0..1+ ; 0 = not known
	CPUEff     float64 `json:"cpuEfficiency"`
	RAMEff     float64 `json:"ramEfficiency"`
	// What usage-sized requests would save over the window, from requested vs used.
	Waste float64 `json:"waste"`
}

type CostReport struct {
	Source    string    `json:"source"` // "opencost/opencost" -- namespace/service it came from
	Window    string    `json:"window"`
	Aggregate string    `json:"aggregate"`
	Rows      []CostRow `json:"rows"`
	Total     float64   `json:"total"`
	Idle      float64   `json:"idle"`
	Monthly   float64   `json:"monthly"` // Total scaled to 30 days
	Waste     float64   `json:"waste"`
	Clusters  []CostRow `json:"clusters,omitempty"` // when OpenCost reads a shared Prometheus: every cluster it knows
	Currency  string    `json:"currency"`
	Age       int       `json:"age"` // seconds since OpenCost was asked (cached for 10 min)
}

// costSource is a located OpenCost service and how to reach it.
type costSource struct {
	ns, svc, pod string
	port         int
	clusterID    string // OpenCost's CLUSTER_ID env, the name it gives this cluster
}

var (
	costMu     sync.Mutex
	costPorts  = map[string]int{} // context -> local port of its forward
	costStops  = map[string]chan struct{}{}
	costSrc    = map[string]*costSource{} // context -> where OpenCost is (nil: looked, none)
	costLooked = map[string]time.Time{}
)

// findCost looks for an OpenCost (or Kubecost cost-analyzer) service.
func (c *Cluster) findCost(ctx context.Context) (*costSource, error) {
	costMu.Lock()
	if t, ok := costLooked[c.Context]; ok && time.Since(t) < 5*time.Minute {
		s := costSrc[c.Context]
		costMu.Unlock()
		return s, nil
	}
	costMu.Unlock()
	svcs, err := c.Typed.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var found *costSource
	for _, s := range svcs.Items {
		name := s.Name
		isOC := name == "opencost" || s.Labels["app.kubernetes.io/name"] == "opencost"
		isKC := name == "kubecost-cost-analyzer" || strings.HasSuffix(name, "-cost-analyzer")
		if !isOC && !isKC {
			continue
		}
		want := 9003 // opencost API
		if isKC {
			want = 9090 // kubecost: the cost-analyzer frontend proxies /model/
		}
		port := 0
		for _, p := range s.Spec.Ports {
			if int(p.Port) == want {
				port = p.TargetPort.IntValue()
				if port == 0 {
					port = int(p.Port)
				}
			}
		}
		if port == 0 || len(s.Spec.Selector) == 0 {
			continue
		}
		pods, err := c.Typed.CoreV1().Pods(s.Namespace).List(ctx, metav1.ListOptions{LabelSelector: labels.SelectorFromSet(s.Spec.Selector).String()})
		if err != nil {
			continue
		}
		for _, p := range pods.Items {
			if p.Status.Phase == corev1.PodRunning {
				found = &costSource{ns: s.Namespace, svc: name, pod: p.Name, port: port}
				for _, ctr := range p.Spec.Containers {
					for _, e := range ctr.Env {
						if e.Name == "CLUSTER_ID" && e.Value != "" {
							found.clusterID = e.Value
						}
					}
				}
				break
			}
		}
		if found != nil {
			break
		}
	}
	costMu.Lock()
	costSrc[c.Context], costLooked[c.Context] = found, time.Now()
	costMu.Unlock()
	return found, nil
}

// costPort returns a local port forwarded to OpenCost, opening it once per cluster.
func (c *Cluster) costPort(src *costSource) (int, error) {
	costMu.Lock()
	if p, ok := costPorts[c.Context]; ok {
		costMu.Unlock()
		// still alive?
		if conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", p), time.Second); err == nil {
			conn.Close()
			return p, nil
		}
		costMu.Lock()
		close(costStops[c.Context])
		delete(costPorts, c.Context)
	}
	costMu.Unlock()
	u, err := url.Parse(c.Config.Host)
	if err != nil {
		return 0, err
	}
	u.Path = strings.TrimRight(u.Path, "/") + fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/portforward", src.ns, src.pod)
	tr, up, err := spdy.RoundTripperFor(c.Config)
	if err != nil {
		return 0, err
	}
	stop, ready := make(chan struct{}), make(chan struct{})
	pf, err := portforward.NewOnAddresses(spdy.NewDialer(up, &http.Client{Transport: tr}, http.MethodPost, u),
		[]string{"127.0.0.1"}, []string{fmt.Sprintf("0:%d", src.port)}, stop, ready, io.Discard, io.Discard)
	if err != nil {
		return 0, err
	}
	errc := make(chan error, 1)
	go func() { errc <- pf.ForwardPorts() }()
	select {
	case <-ready:
	case err := <-errc:
		return 0, err
	case <-time.After(20 * time.Second):
		close(stop)
		return 0, fmt.Errorf("timed out connecting to %s/%s", src.ns, src.pod)
	}
	ports, err := pf.GetPorts()
	if err != nil || len(ports) == 0 {
		close(stop)
		return 0, fmt.Errorf("port-forward to OpenCost reported no port")
	}
	costMu.Lock()
	costPorts[c.Context], costStops[c.Context] = int(ports[0].Local), stop
	costMu.Unlock()
	return int(ports[0].Local), nil
}

// ErrNoCost is returned when the cluster runs no OpenCost / Kubecost.
var ErrNoCost = fmt.Errorf("no OpenCost or Kubecost in this cluster")

type ocAlloc struct {
	Name            string  `json:"name"`
	TotalCost       float64 `json:"totalCost"`
	CPUCost         float64 `json:"cpuCost"`
	RAMCost         float64 `json:"ramCost"`
	PVCost          float64 `json:"pvCost"`
	NetworkCost     float64 `json:"networkCost"`
	LBCost          float64 `json:"loadBalancerCost"`
	GPUCost         float64 `json:"gpuCost"`
	TotalEfficiency float64 `json:"totalEfficiency"`
	CPUEfficiency   float64 `json:"cpuEfficiency"`
	RAMEfficiency   float64 `json:"ramEfficiency"`
	Properties      struct {
		Cluster string `json:"cluster"`
	} `json:"properties"`
}

// Cost reports spend over window ("1d", "7d", "30d", "today", "month"...)
// grouped by aggregate ("namespace", "controller", "pod", "node", "label:team"...),
// optionally only in one namespace. It is limited to this cluster even when
// OpenCost reads a Prometheus shared by several.
func (c *Cluster) Cost(ctx context.Context, window, aggregate, namespace string, fresh bool) (*CostReport, error) {
	key := strings.Join([]string{c.Context, window, aggregate, namespace}, "|")
	costMu.Lock()
	if e, ok := costCache[key]; ok && !fresh && time.Since(e.at) < 10*time.Minute {
		costMu.Unlock()
		r := *e.rep
		r.Age = int(time.Since(e.at).Seconds())
		return &r, nil
	}
	costMu.Unlock()
	// Two people (or a double click) asking for the same figures share one query.
	costMu.Lock()
	if w, ok := costInflight[key]; ok {
		costMu.Unlock()
		<-w.done
		return w.rep, w.err
	}
	w := &costWait{done: make(chan struct{})}
	costInflight[key] = w
	costMu.Unlock()
	rep, err := c.cost(ctx, window, aggregate, namespace)
	costMu.Lock()
	if err == nil {
		costCache[key] = costEntry{rep, time.Now()}
	}
	delete(costInflight, key)
	costMu.Unlock()
	w.rep, w.err = rep, err
	close(w.done)
	return rep, err
}

type costWait struct {
	done chan struct{}
	rep  *CostReport
	err  error
}

var costInflight = map[string]*costWait{}

// PrefetchCost warms the cache for the windows people switch to next, in the
// background, so clicking "7 days" after the 24-hour view is instant.
func (c *Cluster) PrefetchCost(aggregate, namespace string, windows ...string) {
	for _, w := range windows {
		w := w
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()
			_, _ = c.Cost(ctx, w, aggregate, namespace, false)
		}()
	}
}

type costEntry struct {
	rep *CostReport
	at  time.Time
}

var costCache = map[string]costEntry{}

func (c *Cluster) cost(ctx context.Context, window, aggregate, namespace string) (*CostReport, error) {
	if window == "" {
		window = "1d"
	}
	if aggregate == "" {
		aggregate = "namespace"
	}
	src, err := c.findCost(ctx)
	if err != nil {
		return nil, err
	}
	if src == nil {
		return nil, ErrNoCost
	}
	port, err := c.costPort(src)
	if err != nil {
		return nil, fmt.Errorf("reaching OpenCost at %s/%s: %w", src.ns, src.svc, err)
	}
	prefix := ""
	if strings.HasSuffix(src.svc, "cost-analyzer") {
		prefix = "/model"
	}
	get := func(win, agg, filter string) (map[string]ocAlloc, error) {
		q := url.Values{"window": {win}, "aggregate": {agg}, "accumulate": {"true"}, "includeIdle": {"true"}}
		if filter != "" {
			q.Set("filter", filter)
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s/allocation/compute?%s", port, prefix, q.Encode()), nil)
		resp, err := (&http.Client{Timeout: 170 * time.Second}).Do(req)
		if err != nil {
			if strings.Contains(err.Error(), "EOF") || strings.Contains(err.Error(), "connection reset") {
				return nil, errOpenCostDropped
			}
			return nil, err
		}
		defer resp.Body.Close()
		var body struct {
			Code    int                  `json:"code"`
			Message string               `json:"message"`
			Data    []map[string]ocAlloc `json:"data"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&body); err != nil {
			return nil, fmt.Errorf("OpenCost answered something unexpected: %w", err)
		}
		if body.Code != 200 {
			return nil, fmt.Errorf("OpenCost: %s", firstLine(body.Message))
		}
		if len(body.Data) == 0 {
			return map[string]ocAlloc{}, nil
		}
		return body.Data[0], nil
	}
	// One query, for this cluster only. OpenCost often reads a Prometheus shared by
	// several clusters, and asking it for every cluster's totals first costs ~5x the
	// time (measured: 7 days, all clusters 53 s; this cluster 12-19 s). The cluster's
	// name in OpenCost is its CLUSTER_ID, read once from the OpenCost pod's env; when
	// unknown, the context's short name is tried, then no filter.
	shortName := c.Context
	if i := strings.LastIndex(shortName, "/"); i >= 0 {
		shortName = shortName[i+1:]
	}
	id := src.clusterID
	if id == "" {
		id = shortName
	}
	filterFor := func(cluster string) string {
		f := ""
		if cluster != "" {
			f = fmt.Sprintf("cluster:%q", cluster)
		}
		if nss := SplitNamespaces(namespace); len(nss) > 0 {
			if f != "" {
				f += "+"
			}
			q := make([]string, len(nss))
			for i, n := range nss {
				q[i] = fmt.Sprintf("%q", n)
			}
			f += "namespace:" + strings.Join(q, ",") // OpenCost: any of these
		}
		return f
	}
	data, err := getWindow(get, window, aggregate, filterFor(id))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 && src.clusterID == "" {
		// The guess was wrong (OpenCost names the cluster differently): this OpenCost
		// only sees its own cluster anyway when it has no CLUSTER_ID, so ask unfiltered.
		if data, err = getWindow(get, window, aggregate, filterFor("")); err != nil {
			return nil, err
		}
	}
	rep := &CostReport{Source: src.ns + "/" + src.svc, Window: window, Aggregate: aggregate, Currency: "USD", Rows: []CostRow{}}
	for name, a := range data {
		row := toCostRow(name, a)
		if name == "__idle__" {
			rep.Idle += row.Total
			rep.Total += row.Total
			continue
		}
		rep.Rows = append(rep.Rows, row)
		rep.Total += row.Total
		rep.Waste += row.Waste
	}
	sort.Slice(rep.Rows, func(i, j int) bool { return rep.Rows[i].Total > rep.Rows[j].Total })
	if d := windowDays(window); d > 0 {
		rep.Monthly = rep.Total / d * 30
	}
	return rep, nil
}

func toCostRow(name string, a ocAlloc) CostRow {
	r := CostRow{Name: name, Total: a.TotalCost, CPU: a.CPUCost, RAM: a.RAMCost, PV: a.PVCost, Network: a.NetworkCost,
		LB: a.LBCost, GPU: a.GPUCost, Efficiency: a.TotalEfficiency, CPUEff: a.CPUEfficiency, RAMEff: a.RAMEfficiency}
	// Requests far above use: the CPU and memory cost not used. (Efficiency > 1 means
	// use above request -- nothing to save there.)
	if a.CPUEfficiency > 0 && a.CPUEfficiency < 1 {
		r.Waste += a.CPUCost * (1 - a.CPUEfficiency)
	}
	if a.RAMEfficiency > 0 && a.RAMEfficiency < 1 {
		r.Waste += a.RAMCost * (1 - a.RAMEfficiency)
	}
	return r
}

func windowDays(w string) float64 {
	switch w {
	case "today":
		return float64(time.Now().Hour())/24 + 0.01
	case "yesterday":
		return 1
	case "week":
		return float64(time.Now().Weekday()) + 1
	case "month":
		return float64(time.Now().Day())
	}
	var n float64
	var unit string
	if _, err := fmt.Sscanf(w, "%g%s", &n, &unit); err != nil {
		return 0
	}
	switch unit {
	case "d":
		return n
	case "h":
		return n / 24
	case "w":
		return n * 7
	}
	return 0
}

// errOpenCostDropped: OpenCost closed the connection mid-answer -- in practice it was
// OOM-killed (seen on shop-prod: 1 GiB limit, 30-day query over 87 nodes).
var errOpenCostDropped = fmt.Errorf("OpenCost stopped answering (it likely ran out of memory: check `kubectl -n opencost get pods` for OOMKilled restarts and raise its memory limit)")

type costGetter func(window, aggregate, filter string) (map[string]ocAlloc, error)

// getWindow asks for a long window one day at a time and adds the days up. One big
// query makes OpenCost hold the whole period in memory at once, which is what made it
// crash on large clusters; a day at a time stays small, a few run in parallel, and the
// totals are the same. Up to 24 hours is asked directly.
func getWindow(get costGetter, window, aggregate, filter string) (map[string]ocAlloc, error) {
	days := windowDays(window)
	if days <= 1.01 {
		return get(window, aggregate, filter)
	}
	end := time.Now().UTC().Truncate(time.Hour)
	start := end.Add(-time.Duration(days * 24 * float64(time.Hour)))
	if window == "month" {
		y, m, _ := end.Date()
		start = time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
	}
	type span struct{ from, to time.Time }
	var spans []span
	for t := start; t.Before(end); t = t.Add(24 * time.Hour) {
		to := t.Add(24 * time.Hour)
		if to.After(end) {
			to = end
		}
		spans = append(spans, span{t, to})
	}
	results := make([]map[string]ocAlloc, len(spans))
	errs := make([]error, len(spans))
	sem := make(chan struct{}, 3) // gentle on OpenCost
	var wg sync.WaitGroup
	for i, sp := range spans {
		wg.Add(1)
		go func(i int, sp span) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			w := sp.from.Format(time.RFC3339) + "," + sp.to.Format(time.RFC3339)
			results[i], errs[i] = get(w, aggregate, filter)
		}(i, sp)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return sumAllocs(results), nil
}

// sumAllocs adds per-day allocations up. Costs add; efficiencies are weighted by the
// cost they apply to, so a busy day counts more than a quiet one.
func sumAllocs(days []map[string]ocAlloc) map[string]ocAlloc {
	out := map[string]ocAlloc{}
	cpuW, ramW, totW := map[string]float64{}, map[string]float64{}, map[string]float64{}
	for _, d := range days {
		for k, a := range d {
			o := out[k]
			o.Name = k
			o.TotalCost += a.TotalCost
			o.CPUCost += a.CPUCost
			o.RAMCost += a.RAMCost
			o.PVCost += a.PVCost
			o.NetworkCost += a.NetworkCost
			o.LBCost += a.LBCost
			o.GPUCost += a.GPUCost
			if o.Properties.Cluster == "" {
				o.Properties.Cluster = a.Properties.Cluster
			}
			cpuW[k] += a.CPUEfficiency * a.CPUCost
			ramW[k] += a.RAMEfficiency * a.RAMCost
			totW[k] += a.TotalEfficiency * (a.CPUCost + a.RAMCost)
			out[k] = o
		}
	}
	for k, o := range out {
		if o.CPUCost > 0 {
			o.CPUEfficiency = cpuW[k] / o.CPUCost
		}
		if o.RAMCost > 0 {
			o.RAMEfficiency = ramW[k] / o.RAMCost
		}
		if o.CPUCost+o.RAMCost > 0 {
			o.TotalEfficiency = totW[k] / (o.CPUCost + o.RAMCost)
		}
		out[k] = o
	}
	return out
}
