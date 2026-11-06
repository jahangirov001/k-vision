package kube

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
)

// Explain answers "why is this pod not running?" in a sentence, from the pod's own
// state and its events -- the thing a person reads three screens of describe output
// to work out. It is a list because a pod can be wrong in several ways at once.
func (c *Cluster) Explain(ctx context.Context, namespace, pod string) ([]string, error) {
	p, err := c.Typed.CoreV1().Pods(namespace).Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	evs, _ := c.Typed.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{
		FieldSelector: fields.Set{"involvedObject.name": pod, "involvedObject.kind": "Pod"}.String(),
	})
	var events []corev1.Event
	if evs != nil {
		events = evs.Items
	}
	return explainPod(p, events), nil
}

var insufficient = regexp.MustCompile(`Insufficient ([A-Za-z0-9./_-]*[A-Za-z0-9])`)

func explainPod(p *corev1.Pod, events []corev1.Event) []string {
	var out []string
	sort.Slice(events, func(i, j int) bool { return eventTime(events[i]).After(eventTime(events[j])) })
	latest := func(reason string) *corev1.Event {
		for i := range events {
			if events[i].Reason == reason {
				return &events[i]
			}
		}
		return nil
	}

	if p.DeletionTimestamp != nil {
		out = append(out, fmt.Sprintf("Being deleted since %s; if it is stuck, a finalizer is holding it: %v",
			HumanAge(p.DeletionTimestamp.Time), p.Finalizers))
	}
	if p.Status.Phase == corev1.PodPending && p.Spec.NodeName == "" {
		if e := latest("FailedScheduling"); e != nil {
			msg := e.Message
			var res []string
			for _, m := range insufficient.FindAllStringSubmatch(msg, -1) {
				res = append(res, m[1])
			}
			switch {
			case len(res) > 0:
				out = append(out, fmt.Sprintf(
					"Unschedulable: no node has enough %s. If Karpenter manages this cluster, check "+
						"the NodePool limits (:nodepools) and the nodeclaims for launch errors.",
					strings.Join(unique(res), ", ")))
			case strings.Contains(msg, "untolerated taint") || strings.Contains(msg, "taint"):
				out = append(out, "Unschedulable: every node has a taint this pod does not tolerate. "+
					"Add the toleration, or check the pod's nodeSelector/affinity matches a NodePool.")
			case strings.Contains(msg, "affinity") || strings.Contains(msg, "selector"):
				out = append(out, "Unschedulable: no node matches the pod's nodeSelector/affinity.")
			case strings.Contains(msg, "persistentvolumeclaim") || strings.Contains(msg, "volume node affinity"):
				out = append(out, "Unschedulable: its volume is bound to a zone with no available node.")
			default:
				out = append(out, "Unschedulable: "+msg)
			}
		} else {
			out = append(out, "Pending and not yet scheduled; no scheduling event recorded yet.")
		}
	}
	all := append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...)
	for _, cs := range all {
		w, t, last := cs.State.Waiting, cs.State.Terminated, cs.LastTerminationState.Terminated
		switch {
		case w != nil && (w.Reason == "ImagePullBackOff" || w.Reason == "ErrImagePull"):
			out = append(out, fmt.Sprintf("%s: cannot pull image %s — %s. Check the tag exists and the "+
				"node role / imagePullSecret can read the registry.", cs.Name, cs.Image, firstLine(w.Message)))
		case w != nil && w.Reason == "CrashLoopBackOff":
			why := "it keeps exiting"
			if last != nil {
				why = fmt.Sprintf("last exit code %d (%s)", last.ExitCode, last.Reason)
				if last.Reason == "OOMKilled" {
					why += " — it ran out of memory; raise the memory limit or find the leak"
				}
			}
			out = append(out, fmt.Sprintf("%s: crash-looping after %d restarts, %s. Read the previous "+
				"logs (P in the log view).", cs.Name, cs.RestartCount, why))
		case w != nil && w.Reason == "CreateContainerConfigError":
			out = append(out, fmt.Sprintf("%s: cannot start — %s. Usually a missing ConfigMap or Secret key.",
				cs.Name, firstLine(w.Message)))
		case t != nil && t.Reason == "OOMKilled":
			out = append(out, fmt.Sprintf("%s: killed for exceeding its memory limit.", cs.Name))
		case t != nil && t.ExitCode != 0:
			// A finished Job/CronJob run: nothing is waiting or restarting, the process
			// just exited non-zero, and the answer is in its own output.
			msg := fmt.Sprintf("%s: exited with code %d (%s) %s ago", cs.Name, t.ExitCode, t.Reason,
				HumanAge(t.FinishedAt.Time))
			if hint := exitHint(t.ExitCode); hint != "" {
				msg += " — " + hint
			}
			if m := firstLine(t.Message); m != "" {
				msg += ": " + m
			}
			out = append(out, msg+". Its logs say why (l).")
		case !cs.Ready && cs.State.Running != nil:
			if e := latest("Unhealthy"); e != nil {
				out = append(out, fmt.Sprintf("%s: running but not ready — %s", cs.Name, firstLine(e.Message)))
			} else {
				out = append(out, fmt.Sprintf("%s: running but its readiness probe has not passed yet.", cs.Name))
			}
		}
	}
	if e := latest("FailedMount"); e != nil {
		out = append(out, "Volume mount failing: "+firstLine(e.Message))
	}
	if len(out) == 0 {
		if p.Status.Phase == corev1.PodRunning || p.Status.Phase == corev1.PodSucceeded {
			out = append(out, fmt.Sprintf("Nothing wrong found: %s.", p.Status.Phase))
		} else {
			out = append(out, fmt.Sprintf("Phase %s, no specific cause found in its status or events.", p.Status.Phase))
		}
	}
	return out
}

// exitHint names the exit codes that mean something by themselves.
func exitHint(code int32) string {
	switch code {
	case 137:
		return "SIGKILL: OOM-killed or stopped by the kubelet"
	case 143:
		return "SIGTERM: stopped, e.g. by a rollout, eviction or activeDeadlineSeconds"
	case 139:
		return "SIGSEGV: the process crashed"
	case 126, 127:
		return "the command could not be run (not found / not executable)"
	}
	return ""
}

func eventTime(e corev1.Event) time.Time {
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Time
	}
	if !e.EventTime.IsZero() {
		return e.EventTime.Time
	}
	return e.CreationTimestamp.Time
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 240 {
		s = s[:240] + "…"
	}
	return s
}

func unique(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// ClusterRow is a row tagged with the cluster it came from, for multi-cluster views.
type ClusterRow struct {
	Context string `json:"context"`
	Row
}

// ClusterResult is one cluster's share of a multi-cluster query. Errors are per
// cluster: one unreachable cluster (an expired SSO session for one account) must not
// blank the whole view.
type ClusterResult struct {
	Context string       `json:"context"`
	Rows    []ClusterRow `json:"rows"`
	Columns []string     `json:"columns"`
	Error   string       `json:"error,omitempty"`
}

// ListEverywhere runs one List across many contexts in parallel. `filter`, when set,
// keeps rows whose name contains it, or that are unhealthy when it is "!ok".
func (m *Manager) ListEverywhere(ctx context.Context, contexts []string, kind, namespace, filter string) []ClusterResult {
	out := make([]ClusterResult, len(contexts))
	var wg sync.WaitGroup
	for i, name := range contexts {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			res := ClusterResult{Context: name}
			defer func() { out[i] = res }()
			cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			c, err := m.Cluster(name)
			if err != nil {
				res.Error = err.Error()
				return
			}
			k, err := c.ResolveKind(kind)
			if err != nil {
				res.Error = err.Error()
				return
			}
			rows, cols, err := c.List(cctx, k, namespace)
			if err != nil {
				res.Error = err.Error()
				return
			}
			res.Columns = cols
			f := ParseFilter(filter)
			for _, r := range rows {
				if !f.Match(name, r) {
					continue
				}
				res.Rows = append(res.Rows, ClusterRow{Context: name, Row: r})
			}
		}(i, name)
	}
	wg.Wait()
	return out
}
