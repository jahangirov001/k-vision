package kube

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// columnsFor is the per-kind part of a table. Every kind gets NAMESPACE, NAME, STATUS
// and AGE; these are what make a pod list or an ArgoCD list worth reading.
func columnsFor(k Kind) []string {
	switch k.ID() {
	case "pods":
		return []string{"READY", "RESTARTS", "CPU", "MEMORY", "NODE", "IP"}
	case "deployments.apps", "statefulsets.apps":
		return []string{"READY", "UP-TO-DATE", "IMAGE"}
	case "daemonsets.apps":
		return []string{"DESIRED", "READY", "IMAGE"}
	case "replicasets.apps":
		return []string{"DESIRED", "READY"}
	case "nodes":
		return []string{"ROLES", "VERSION", "INSTANCE", "ZONE", "NODEPOOL"}
	case "services":
		return []string{"TYPE", "CLUSTER-IP", "EXTERNAL", "PORTS"}
	case "ingresses.networking.k8s.io":
		return []string{"CLASS", "HOSTS", "ADDRESS"}
	case "jobs.batch":
		return []string{"COMPLETIONS", "DURATION"}
	case "cronjobs.batch":
		return []string{"SCHEDULE", "SUSPEND", "LAST"}
	case "persistentvolumeclaims":
		return []string{"VOLUME", "CAPACITY", "CLASS"}
	case "events", "events.k8s.io":
		return []string{"TYPE", "REASON", "OBJECT", "MESSAGE"}
	case "secrets":
		return []string{"TYPE", "KEYS", "LABELS"}
	case "configmaps":
		return []string{"KEYS", "LABELS"}
	case "applications.argoproj.io":
		return []string{"SYNC", "HEALTH", "PROJECT", "REPO", "REVISION", "DESTINATION", "LAST SYNC"}
	case "applicationsets.argoproj.io":
		return []string{"GENERATORS", "APPLICATIONS", "TEMPLATE"}
	case "appprojects.argoproj.io":
		return []string{"DESTINATIONS", "REPOS", "DESCRIPTION"}
	case "scaledjobs.keda.sh":
		return []string{"TRIGGERS", "MAX", "STRATEGY"}
	case "triggerauthentications.keda.sh", "clustertriggerauthentications.keda.sh":
		return []string{"SOURCES"}
	case "verticalpodautoscalers.autoscaling.k8s.io":
		return []string{"TARGET", "MODE", "CPU", "MEMORY"}
	case "ec2nodeclasses.karpenter.k8s.aws":
		return []string{"AMI", "ROLE", "SUBNETS", "SECURITY GROUPS"}
	case "nodeoverlays.karpenter.sh":
		return []string{"WEIGHT", "PRICE"}
	case "nodepools.karpenter.sh":
		return []string{"NODES", "CPU", "MEMORY", "LIMIT-CPU"}
	case "nodeclaims.karpenter.sh":
		return []string{"TYPE", "CAPACITY", "ZONE", "NODE"}
	case "scaledobjects.keda.sh":
		return []string{"TARGET", "MIN", "MAX", "TRIGGERS", "ACTIVE"}
	case "horizontalpodautoscalers.autoscaling":
		return []string{"TARGET", "MIN", "MAX", "REPLICAS", "METRICS"}
	case "ingressclasses.networking.k8s.io":
		return []string{"CONTROLLER", "DEFAULT"}
	case "networkpolicies.networking.k8s.io":
		return []string{"POD-SELECTOR", "TYPES"}
	case "poddisruptionbudgets.policy":
		return []string{"MIN-AVAILABLE", "MAX-UNAVAILABLE", "ALLOWED", "PODS"}
	case "resourcequotas":
		return []string{"USED"}
	case "priorityclasses.scheduling.k8s.io":
		return []string{"VALUE", "DEFAULT", "PREEMPTION"}
	case "roles.rbac.authorization.k8s.io", "clusterroles.rbac.authorization.k8s.io":
		return []string{"RULES"}
	case "rolebindings.rbac.authorization.k8s.io", "clusterrolebindings.rbac.authorization.k8s.io":
		return []string{"ROLE", "SUBJECTS"}
	case "serviceaccounts":
		return []string{"SECRETS", "IRSA-ROLE"}
	}
	return nil
}

func toRow(k Kind, u *unstructured.Unstructured) Row {
	created := u.GetCreationTimestamp().Time
	r := Row{
		Namespace: u.GetNamespace(),
		Name:      u.GetName(),
		Created:   created,
		Age:       HumanAge(created),
		Labels:    u.GetLabels(),
		Cells:     map[string]string{},
	}
	o := u.Object
	switch k.ID() {
	case "pods":
		podRow(&r, o)
	case "deployments.apps", "statefulsets.apps":
		want := num(o, "spec", "replicas")
		ready := num(o, "status", "readyReplicas")
		r.Cells["READY"] = fmt.Sprintf("%d/%d", ready, want)
		r.Cells["UP-TO-DATE"] = fmt.Sprint(num(o, "status", "updatedReplicas"))
		r.Cells["IMAGE"] = firstImage(o, "spec", "template", "spec", "containers")
		r.Status, r.Health = replicaHealth(ready, want)
	case "daemonsets.apps":
		want, ready := num(o, "status", "desiredNumberScheduled"), num(o, "status", "numberReady")
		r.Cells["DESIRED"], r.Cells["READY"] = fmt.Sprint(want), fmt.Sprint(ready)
		r.Cells["IMAGE"] = firstImage(o, "spec", "template", "spec", "containers")
		r.Status, r.Health = replicaHealth(ready, want)
	case "replicasets.apps":
		want, ready := num(o, "spec", "replicas"), num(o, "status", "readyReplicas")
		r.Cells["DESIRED"], r.Cells["READY"] = fmt.Sprint(want), fmt.Sprint(ready)
		r.Status, r.Health = replicaHealth(ready, want)
	case "nodes":
		nodeRow(&r, o)
	case "services":
		r.Cells["TYPE"] = str(o, "spec", "type")
		r.Cells["CLUSTER-IP"] = str(o, "spec", "clusterIP")
		r.Cells["EXTERNAL"] = lbAddress(o)
		r.Cells["PORTS"] = servicePorts(o)
	case "ingresses.networking.k8s.io":
		r.Cells["CLASS"] = str(o, "spec", "ingressClassName")
		r.Cells["HOSTS"] = ingressHosts(o)
		r.Cells["ADDRESS"] = lbAddress(o)
	case "jobs.batch":
		jobRow(&r, o)
	case "cronjobs.batch":
		r.Cells["SCHEDULE"] = str(o, "spec", "schedule")
		r.Cells["SUSPEND"] = fmt.Sprint(boolean(o, "spec", "suspend"))
		r.Cells["LAST"] = str(o, "status", "lastScheduleTime")
	case "persistentvolumeclaims":
		r.Cells["VOLUME"] = str(o, "spec", "volumeName")
		r.Cells["CAPACITY"] = str(o, "status", "capacity", "storage")
		r.Cells["CLASS"] = str(o, "spec", "storageClassName")
		r.Status = str(o, "status", "phase")
		r.Health = map[string]Health{"Bound": Healthy, "Pending": Warning, "Lost": Failing}[r.Status]
	case "events", "events.k8s.io":
		eventRow(&r, o)
	case "secrets":
		r.Cells["TYPE"] = orDash(str(o, "type"))
		data, _ := lookup(o, "data").(map[string]any)
		r.Cells["KEYS"] = fmt.Sprint(len(data))
		r.Cells["LABELS"] = labelText(u.GetLabels())
	case "configmaps":
		data, _ := lookup(o, "data").(map[string]any)
		bin, _ := lookup(o, "binaryData").(map[string]any)
		r.Cells["KEYS"] = fmt.Sprint(len(data) + len(bin))
		r.Cells["LABELS"] = labelText(u.GetLabels())
	case "applications.argoproj.io":
		argoRow(&r, o)
	case "applicationsets.argoproj.io":
		appSetRow(&r, o)
	case "appprojects.argoproj.io":
		projectRow(&r, o)
	case "scaledjobs.keda.sh":
		r.Cells["TRIGGERS"] = triggerTypes(o)
		r.Cells["MAX"] = str(o, "spec", "maxReplicaCount")
		r.Cells["STRATEGY"] = orDash(str(o, "spec", "scalingStrategy", "strategy"))
		r.Status, r.Health = readyCondition(o)
	case "triggerauthentications.keda.sh", "clustertriggerauthentications.keda.sh":
		r.Cells["SOURCES"] = authSources(o)
	case "verticalpodautoscalers.autoscaling.k8s.io":
		r.Cells["TARGET"] = str(o, "spec", "targetRef", "name")
		r.Cells["MODE"] = orDash(str(o, "spec", "updatePolicy", "updateMode"))
		r.Cells["CPU"], r.Cells["MEMORY"] = vpaTarget(o)
		r.Status, r.Health = conditionHealth(o, "RecommendationProvided")
	case "ec2nodeclasses.karpenter.k8s.aws":
		r.Cells["AMI"] = amiSelector(o)
		r.Cells["ROLE"] = orDash(str(o, "spec", "role"))
		r.Cells["SUBNETS"] = fmt.Sprint(len(sliceAt(o, "status", "subnets")))
		r.Cells["SECURITY GROUPS"] = fmt.Sprint(len(sliceAt(o, "status", "securityGroups")))
		r.Status, r.Health = readyCondition(o)
	case "nodeoverlays.karpenter.sh":
		r.Cells["WEIGHT"] = orDash(str(o, "spec", "weight"))
		r.Cells["PRICE"] = orDash(firstNonEmpty(str(o, "spec", "price"), str(o, "spec", "priceAdjustment")))
	case "nodepools.karpenter.sh":
		r.Cells["NODES"] = str(o, "status", "resources", "nodes")
		r.Cells["CPU"] = str(o, "status", "resources", "cpu")
		r.Cells["MEMORY"] = str(o, "status", "resources", "memory")
		r.Cells["LIMIT-CPU"] = str(o, "spec", "limits", "cpu")
		r.Status, r.Health = readyCondition(o)
	case "nodeclaims.karpenter.sh":
		r.Cells["TYPE"] = label(u, "node.kubernetes.io/instance-type")
		r.Cells["CAPACITY"] = label(u, "karpenter.sh/capacity-type")
		r.Cells["ZONE"] = label(u, "topology.kubernetes.io/zone")
		r.Cells["NODE"] = str(o, "status", "nodeName")
		r.Status, r.Health = readyCondition(o)
	case "scaledobjects.keda.sh":
		r.Cells["TARGET"] = str(o, "spec", "scaleTargetRef", "name")
		r.Cells["MIN"] = str(o, "spec", "minReplicaCount")
		r.Cells["MAX"] = str(o, "spec", "maxReplicaCount")
		r.Cells["TRIGGERS"] = triggerTypes(o)
		r.Cells["ACTIVE"] = conditionStatus(o, "Active")
		r.Status, r.Health = readyCondition(o)
	case "horizontalpodautoscalers.autoscaling":
		r.Cells["TARGET"] = str(o, "spec", "scaleTargetRef", "name")
		r.Cells["MIN"] = str(o, "spec", "minReplicas")
		r.Cells["MAX"] = str(o, "spec", "maxReplicas")
		r.Cells["REPLICAS"] = fmt.Sprintf("%d → %d", num(o, "status", "currentReplicas"), num(o, "status", "desiredReplicas"))
		r.Cells["METRICS"] = hpaMetrics(o)
		cur, max := num(o, "status", "currentReplicas"), num(o, "spec", "maxReplicas")
		switch {
		case conditionStatus(o, "ScalingActive") == "False":
			r.Status, r.Health = "Not scaling", Failing
		case max > 0 && cur >= max:
			r.Status, r.Health = "At max", Warning
		default:
			r.Status, r.Health = "Scaling", Healthy
		}
	case "ingressclasses.networking.k8s.io":
		r.Cells["CONTROLLER"] = str(o, "spec", "controller")
		r.Cells["DEFAULT"] = yesNo(u.GetAnnotations()["ingressclass.kubernetes.io/is-default-class"] == "true")
	case "networkpolicies.networking.k8s.io":
		r.Cells["POD-SELECTOR"] = selectorText(lookup(o, "spec", "podSelector"))
		types, _ := lookup(o, "spec", "policyTypes").([]any)
		var ts []string
		for _, t := range types {
			ts = append(ts, fmt.Sprint(t))
		}
		r.Cells["TYPES"] = strings.Join(ts, ",")
	case "poddisruptionbudgets.policy":
		r.Cells["MIN-AVAILABLE"] = orDash(str(o, "spec", "minAvailable"))
		r.Cells["MAX-UNAVAILABLE"] = orDash(str(o, "spec", "maxUnavailable"))
		allowed := num(o, "status", "disruptionsAllowed")
		r.Cells["ALLOWED"] = fmt.Sprint(allowed)
		r.Cells["PODS"] = fmt.Sprintf("%d/%d", num(o, "status", "currentHealthy"), num(o, "status", "expectedPods"))
		// A PDB that allows no disruption blocks every drain and node rotation:
		// that is the one worth seeing in yellow.
		switch {
		case num(o, "status", "expectedPods") == 0:
			r.Status, r.Health = "NoPods", Neutral
		case allowed == 0:
			r.Status, r.Health = "BlocksEviction", Warning
		default:
			r.Status, r.Health = "OK", Healthy
		}
	case "resourcequotas":
		r.Cells["USED"] = quotaText(o)
	case "priorityclasses.scheduling.k8s.io":
		r.Cells["VALUE"] = str(o, "value")
		r.Cells["DEFAULT"] = yesNo(boolean(o, "globalDefault"))
		r.Cells["PREEMPTION"] = orDash(str(o, "preemptionPolicy"))
	case "roles.rbac.authorization.k8s.io", "clusterroles.rbac.authorization.k8s.io":
		rules, _ := lookup(o, "rules").([]any)
		r.Cells["RULES"] = fmt.Sprint(len(rules))
	case "rolebindings.rbac.authorization.k8s.io", "clusterrolebindings.rbac.authorization.k8s.io":
		r.Cells["ROLE"] = str(o, "roleRef", "kind") + "/" + str(o, "roleRef", "name")
		subs, _ := lookup(o, "subjects").([]any)
		var names []string
		for _, s := range subs {
			m, _ := s.(map[string]any)
			n := str(m, "kind") + ":" + str(m, "name")
			if ns := str(m, "namespace"); ns != "" {
				n = str(m, "kind") + ":" + ns + "/" + str(m, "name")
			}
			names = append(names, n)
		}
		if len(names) > 3 {
			names = append(names[:3], fmt.Sprintf("+%d", len(names)-3))
		}
		r.Cells["SUBJECTS"] = strings.Join(names, ", ")
	case "serviceaccounts":
		secrets, _ := lookup(o, "secrets").([]any)
		r.Cells["SECRETS"] = fmt.Sprint(len(secrets))
		role := u.GetAnnotations()["eks.amazonaws.com/role-arn"]
		if i := strings.LastIndex(role, "/"); i >= 0 {
			role = role[i+1:]
		}
		r.Cells["IRSA-ROLE"] = orDash(role)
	default:
		// Any CRD: most expose a Ready condition or a phase, and that is enough to
		// colour the row. Crossplane resources are covered by Ready + Synced.
		if p := str(o, "status", "phase"); p != "" {
			r.Status = p
		}
		if s, h := readyCondition(o); h != Neutral {
			r.Status, r.Health = s, h
			if synced := conditionStatus(o, "Synced"); synced == "False" {
				r.Status, r.Health = "NotSynced", Failing
			}
		}
	}
	if r.Status == "" {
		r.Status = "-"
	}
	return r
}

func podRow(r *Row, o map[string]any) {
	statuses, _ := lookup(o, "status", "containerStatuses").([]any)
	ready, restarts := 0, int64(0)
	reason := ""
	for _, s := range statuses {
		cs, _ := s.(map[string]any)
		if boolean(cs, "ready") {
			ready++
		}
		restarts += num(cs, "restartCount")
		// The waiting/terminated reason is what kubectl shows instead of the phase:
		// CrashLoopBackOff, ImagePullBackOff, OOMKilled, Error.
		if w := str(cs, "state", "waiting", "reason"); w != "" && reason == "" {
			reason = w
		}
		if t := str(cs, "state", "terminated", "reason"); t != "" && reason == "" {
			reason = t
		}
	}
	containers, _ := lookup(o, "spec", "containers").([]any)
	r.Cells["READY"] = fmt.Sprintf("%d/%d", ready, len(containers))
	r.Cells["RESTARTS"] = fmt.Sprint(restarts)
	r.Cells["NODE"] = shortNode(str(o, "spec", "nodeName"))
	r.Cells["IP"] = str(o, "status", "podIP")
	phase := str(o, "status", "phase")
	r.Status = phase
	if reason != "" && phase != "Succeeded" {
		r.Status = reason
	}
	if lookup(o, "metadata", "deletionTimestamp") != nil {
		r.Status = "Terminating"
	}
	switch {
	case r.Status == "Running" && ready == len(containers):
		r.Health = Healthy
	case r.Status == "Succeeded" || r.Status == "Completed":
		r.Health = Neutral
	case r.Status == "Pending" || r.Status == "ContainerCreating" || r.Status == "Terminating" ||
		r.Status == "PodInitializing" || r.Status == "Running":
		r.Health = Warning
	default:
		r.Health = Failing
	}
}

func nodeRow(r *Row, o map[string]any) {
	var roles []string
	for k := range r.Labels {
		if strings.HasPrefix(k, "node-role.kubernetes.io/") {
			roles = append(roles, strings.TrimPrefix(k, "node-role.kubernetes.io/"))
		}
	}
	if len(roles) == 0 {
		roles = []string{"<none>"}
	}
	r.Cells["ROLES"] = strings.Join(roles, ",")
	r.Cells["VERSION"] = str(o, "status", "nodeInfo", "kubeletVersion")
	r.Cells["INSTANCE"] = r.Labels["node.kubernetes.io/instance-type"]
	r.Cells["ZONE"] = r.Labels["topology.kubernetes.io/zone"]
	r.Cells["NODEPOOL"] = r.Labels["karpenter.sh/nodepool"]
	r.Status, r.Health = "NotReady", Failing
	if conditionStatus(o, "Ready") == "True" {
		r.Status, r.Health = "Ready", Healthy
	}
	if boolean(o, "spec", "unschedulable") {
		r.Status += ",SchedulingDisabled"
		r.Health = Warning
	}
}

func jobRow(r *Row, o map[string]any) {
	want := num(o, "spec", "completions")
	if want == 0 {
		want = 1
	}
	done := num(o, "status", "succeeded")
	r.Cells["COMPLETIONS"] = fmt.Sprintf("%d/%d", done, want)
	r.Cells["DURATION"] = "-"
	switch {
	case conditionStatus(o, "Failed") == "True":
		r.Status, r.Health = "Failed", Failing
	case done >= want:
		r.Status, r.Health = "Complete", Healthy
	default:
		r.Status, r.Health = "Running", Warning
	}
}

func eventRow(r *Row, o map[string]any) {
	r.Cells["TYPE"] = str(o, "type")
	r.Cells["REASON"] = str(o, "reason")
	obj := str(o, "involvedObject", "kind") + "/" + str(o, "involvedObject", "name")
	if obj == "/" {
		obj = str(o, "regarding", "kind") + "/" + str(o, "regarding", "name")
	}
	r.Cells["OBJECT"] = obj
	msg := str(o, "message")
	if msg == "" {
		msg = str(o, "note")
	}
	r.Cells["MESSAGE"] = msg
	r.Status = r.Cells["TYPE"]
	if r.Status == "Warning" {
		r.Health = Warning
	}
}

func argoRow(r *Row, o map[string]any) {
	sync := str(o, "status", "sync", "status")
	health := str(o, "status", "health", "status")
	r.Cells["SYNC"] = sync
	r.Cells["HEALTH"] = health
	rev := str(o, "status", "sync", "revision")
	if rev == "" {
		if rs := sliceAt(o, "status", "sync", "revisions"); len(rs) > 0 {
			rev = fmt.Sprint(rs[0])
		}
	}
	if len(rev) > 8 {
		rev = rev[:8]
	}
	r.Cells["REVISION"] = rev
	server := firstNonEmpty(str(o, "spec", "destination", "name"), str(o, "spec", "destination", "server"))
	if server == "https://kubernetes.default.svc" {
		server = "in-cluster"
	}
	r.Cells["DESTINATION"] = server + " / " + orDash(str(o, "spec", "destination", "namespace"))
	r.Cells["PROJECT"] = str(o, "spec", "project")
	repo := str(o, "spec", "source", "repoURL")
	if repo == "" {
		if ss := sliceAt(o, "spec", "sources"); len(ss) > 0 {
			m, _ := ss[0].(map[string]any)
			repo = str(m, "repoURL")
			if len(ss) > 1 {
				repo += fmt.Sprintf(" +%d", len(ss)-1)
			}
		}
	}
	r.Cells["REPO"] = shortRepo(repo)
	r.Cells["LAST SYNC"] = orDash(str(o, "status", "operationState", "phase"))
	if t := str(o, "status", "operationState", "finishedAt"); t != "" {
		if tt, err := time.Parse(time.RFC3339, t); err == nil {
			r.Cells["LAST SYNC"] += " " + HumanAge(tt) + " ago"
		}
	}
	r.Status = health + "/" + sync
	switch {
	case health == "Degraded" || health == "Missing":
		r.Health = Failing
	case health == "Healthy" && sync == "Synced":
		r.Health = Healthy
	default:
		r.Health = Warning
	}
}

func replicaHealth(ready, want int64) (string, Health) {
	switch {
	case want == 0:
		return "ScaledDown", Neutral
	case ready >= want:
		return "Available", Healthy
	case ready == 0:
		return "Unavailable", Failing
	default:
		return "Progressing", Warning
	}
}

func readyCondition(o map[string]any) (string, Health) {
	switch conditionStatus(o, "Ready") {
	case "True":
		return "Ready", Healthy
	case "False":
		reason := conditionReason(o, "Ready")
		if reason == "" {
			reason = "NotReady"
		}
		return reason, Failing
	case "Unknown":
		return "Unknown", Warning
	}
	return "", Neutral
}

func conditionStatus(o map[string]any, typ string) string {
	c := condition(o, typ)
	return str(c, "status")
}

func conditionReason(o map[string]any, typ string) string {
	return str(condition(o, typ), "reason")
}

func condition(o map[string]any, typ string) map[string]any {
	conds, _ := lookup(o, "status", "conditions").([]any)
	for _, c := range conds {
		m, _ := c.(map[string]any)
		if str(m, "type") == typ {
			return m
		}
	}
	return nil
}

func lbAddress(o map[string]any) string {
	ing, _ := lookup(o, "status", "loadBalancer", "ingress").([]any)
	var out []string
	for _, i := range ing {
		m, _ := i.(map[string]any)
		if h := str(m, "hostname"); h != "" {
			out = append(out, h)
		} else if ip := str(m, "ip"); ip != "" {
			out = append(out, ip)
		}
	}
	if len(out) == 0 {
		return "-"
	}
	return strings.Join(out, ",")
}

func servicePorts(o map[string]any) string {
	ports, _ := lookup(o, "spec", "ports").([]any)
	var out []string
	for _, p := range ports {
		m, _ := p.(map[string]any)
		s := fmt.Sprintf("%d/%s", num(m, "port"), str(m, "protocol"))
		if np := num(m, "nodePort"); np != 0 {
			s = fmt.Sprintf("%d:%d/%s", num(m, "port"), np, str(m, "protocol"))
		}
		out = append(out, s)
	}
	return strings.Join(out, ",")
}

func ingressHosts(o map[string]any) string {
	rules, _ := lookup(o, "spec", "rules").([]any)
	var out []string
	for _, r := range rules {
		m, _ := r.(map[string]any)
		if h := str(m, "host"); h != "" {
			out = append(out, h)
		}
	}
	if len(out) == 0 {
		return "*"
	}
	return strings.Join(out, ",")
}

func firstImage(o map[string]any, path ...string) string {
	cs, _ := lookup(o, path...).([]any)
	if len(cs) == 0 {
		return ""
	}
	m, _ := cs[0].(map[string]any)
	img := str(m, "image")
	if i := strings.LastIndex(img, "/"); i >= 0 {
		img = img[i+1:] // the registry is the same 60 characters on every row
	}
	if len(cs) > 1 {
		img += fmt.Sprintf(" +%d", len(cs)-1)
	}
	return img
}

func label(u *unstructured.Unstructured, key string) string { return u.GetLabels()[key] }

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return ""
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// selectorText renders a label selector the way kubectl does: "app=web,tier=db",
// "<all>" for the empty selector that matches every pod.
func selectorText(v any) string {
	m, _ := v.(map[string]any)
	labels, _ := m["matchLabels"].(map[string]any)
	exprs, _ := m["matchExpressions"].([]any)
	if len(labels) == 0 && len(exprs) == 0 {
		return "<all>"
	}
	var out []string
	for k, val := range labels {
		out = append(out, k+"="+fmt.Sprint(val))
	}
	sort.Strings(out)
	if len(exprs) > 0 {
		out = append(out, fmt.Sprintf("+%d expr", len(exprs)))
	}
	return strings.Join(out, ",")
}

// quotaText is "cpu 3/8, memory 4Gi/16Gi, pods 12/50" for a ResourceQuota.
func quotaText(o map[string]any) string {
	hard, _ := lookup(o, "status", "hard").(map[string]any)
	used, _ := lookup(o, "status", "used").(map[string]any)
	keys := make([]string, 0, len(hard))
	for k := range hard {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		out = append(out, fmt.Sprintf("%s %v/%v", k, used[k], hard[k]))
	}
	return strings.Join(out, ", ")
}

// shortNode drops the EC2 DNS suffix: "ip-10-1-2-3.eu-central-1.compute.internal" is
// the same 30 characters on every row and pushes the useful columns off screen.
func shortNode(n string) string {
	if i := strings.Index(n, ".compute.internal"); i > 0 {
		if j := strings.IndexByte(n, '.'); j > 0 && j < i {
			return n[:j]
		}
	}
	if i := strings.Index(n, ".ec2.internal"); i > 0 {
		return n[:i]
	}
	return n
}

func lookup(o map[string]any, path ...string) any {
	var cur any = o
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

func str(o map[string]any, path ...string) string {
	switch v := lookup(o, path...).(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}

func num(o map[string]any, path ...string) int64 {
	switch v := lookup(o, path...).(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	}
	return 0
}

func boolean(o map[string]any, path ...string) bool {
	b, _ := lookup(o, path...).(bool)
	return b
}

func sliceAt(o map[string]any, path ...string) []any {
	v, _ := lookup(o, path...).([]any)
	return v
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// shortRepo turns git@gitlab.com:Org/Infra/gitops/app.git into gitlab.com/…/app.
func shortRepo(u string) string {
	if u == "" {
		return "-"
	}
	u = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "git@"), ".git")
	u = strings.Replace(u, ":", "/", 1)
	parts := strings.Split(u, "/")
	if len(parts) > 3 {
		return parts[0] + "/…/" + strings.Join(parts[len(parts)-2:], "/")
	}
	return u
}

func appSetRow(r *Row, o map[string]any) {
	var gens []string
	for _, g := range sliceAt(o, "spec", "generators") {
		m, _ := g.(map[string]any)
		for k := range m {
			gens = append(gens, k)
		}
	}
	sort.Strings(gens)
	r.Cells["GENERATORS"] = orDash(strings.Join(gens, ", "))
	apps := sliceAt(o, "status", "resources")
	bad := 0
	for _, a := range apps {
		m, _ := a.(map[string]any)
		if h := str(m, "health", "status"); h == "Degraded" || h == "Missing" {
			bad++
		}
	}
	r.Cells["APPLICATIONS"] = fmt.Sprint(len(apps))
	if bad > 0 {
		r.Cells["APPLICATIONS"] += fmt.Sprintf(" (%d degraded)", bad)
	}
	r.Cells["TEMPLATE"] = orDash(str(o, "spec", "template", "metadata", "name"))
	switch {
	case conditionStatus(o, "ErrorOccurred") == "True":
		r.Status, r.Health = "Error", Failing
	case bad > 0:
		r.Status, r.Health = "Apps degraded", Warning
	case conditionStatus(o, "ResourcesUpToDate") == "True":
		r.Status, r.Health = "Up to date", Healthy
	default:
		r.Status, r.Health = "Progressing", Warning
	}
}

func projectRow(r *Row, o map[string]any) {
	var dest []string
	for _, d := range sliceAt(o, "spec", "destinations") {
		m, _ := d.(map[string]any)
		srv := firstNonEmpty(str(m, "name"), str(m, "server"))
		if srv == "https://kubernetes.default.svc" {
			srv = "in-cluster"
		}
		dest = append(dest, srv+"/"+str(m, "namespace"))
	}
	r.Cells["DESTINATIONS"] = orDash(strings.Join(dest, ", "))
	var repos []string
	for _, x := range sliceAt(o, "spec", "sourceRepos") {
		repos = append(repos, shortRepo(fmt.Sprint(x)))
	}
	r.Cells["REPOS"] = orDash(strings.Join(repos, ", "))
	r.Cells["DESCRIPTION"] = orDash(str(o, "spec", "description"))
}

func triggerTypes(o map[string]any) string {
	var t []string
	for _, x := range sliceAt(o, "spec", "triggers") {
		m, _ := x.(map[string]any)
		t = append(t, str(m, "type"))
	}
	return orDash(strings.Join(t, ", "))
}

func authSources(o map[string]any) string {
	spec, _ := lookup(o, "spec").(map[string]any)
	var s []string
	for k := range spec {
		s = append(s, k)
	}
	sort.Strings(s)
	return orDash(strings.Join(s, ", "))
}

func hpaMetrics(o map[string]any) string {
	var m []string
	for _, x := range sliceAt(o, "spec", "metrics") {
		mm, _ := x.(map[string]any)
		typ := str(mm, "type")
		switch typ {
		case "Resource":
			name := str(mm, "resource", "name")
			if v := str(mm, "resource", "target", "averageUtilization"); v != "" {
				name += " " + v + "%"
			}
			m = append(m, name)
		case "External":
			m = append(m, str(mm, "external", "metric", "name"))
		default:
			m = append(m, strings.ToLower(typ))
		}
	}
	return orDash(strings.Join(m, ", "))
}

func vpaTarget(o map[string]any) (string, string) {
	recs := sliceAt(o, "status", "recommendation", "containerRecommendations")
	if len(recs) == 0 {
		return "-", "-"
	}
	m, _ := recs[0].(map[string]any)
	return orDash(str(m, "target", "cpu")), orDash(str(m, "target", "memory"))
}

func conditionHealth(o map[string]any, typ string) (string, Health) {
	switch conditionStatus(o, typ) {
	case "True":
		return "Ready", Healthy
	case "False":
		return "Not ready", Warning
	}
	return "", Neutral
}

func amiSelector(o map[string]any) string {
	if a := str(o, "spec", "amiFamily"); a != "" {
		return a
	}
	for _, x := range sliceAt(o, "spec", "amiSelectorTerms") {
		m, _ := x.(map[string]any)
		if a := firstNonEmpty(str(m, "alias"), str(m, "id"), str(m, "name")); a != "" {
			return a
		}
	}
	return "-"
}

// labelText is the labels worth a glance in a list: app / instance / owner first,
// at most three, the rest counted.
func labelText(l map[string]string) string {
	if len(l) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	pri := func(k string) int {
		switch {
		case k == "app.kubernetes.io/name" || k == "app":
			return 0
		case k == "app.kubernetes.io/instance" || k == "owner" || k == "name":
			return 1
		case strings.HasPrefix(k, "app.kubernetes.io/"):
			return 2
		}
		return 3
	}
	sort.Slice(keys, func(i, j int) bool {
		if pri(keys[i]) != pri(keys[j]) {
			return pri(keys[i]) < pri(keys[j])
		}
		return keys[i] < keys[j]
	})
	var parts []string
	for _, k := range keys[:min(3, len(keys))] {
		short := k
		if i := strings.LastIndex(k, "/"); i >= 0 {
			short = k[i+1:]
		}
		parts = append(parts, short+"="+l[k])
	}
	if len(keys) > 3 {
		parts = append(parts, fmt.Sprintf("+%d", len(keys)-3))
	}
	return strings.Join(parts, " ")
}
