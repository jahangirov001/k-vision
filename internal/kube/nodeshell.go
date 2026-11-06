package kube

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
)

// NodeShellImage is the image of the helper pod; KV_NODE_SHELL_IMAGE overrides it for
// clusters that can only pull from a private registry.
func NodeShellImage() string {
	if img := os.Getenv("KV_NODE_SHELL_IMAGE"); img != "" {
		return img
	}
	return "public.ecr.aws/docker/library/busybox:1.36"
}

// NodeShellNamespace is where helper pods are created; KV_NODE_SHELL_NAMESPACE
// overrides it.
func NodeShellNamespace() string {
	if ns := os.Getenv("KV_NODE_SHELL_NAMESPACE"); ns != "" {
		return ns
	}
	return "default"
}

const nodeShellLabel = "k-vision/node-shell"

// StartNodeShell does what `kubectl debug node/<name>` does: it schedules a pod onto
// the node that shares the host's process namespace, with the host's filesystem at
// /host, and returns once it is running. The caller execs into it and then calls
// StopNodeShell. The pod is labelled so a leftover from a crash is easy to find.
func (c *Cluster) StartNodeShell(ctx context.Context, node string) (ns, pod string, err error) {
	a := Audit{Context: c.Context, Action: "node-shell", Kind: "nodes", Name: node}
	defer func() {
		a.Detail = fmt.Sprintf("pod=%s/%s image=%s", ns, pod, NodeShellImage())
		record(a, err)
	}()
	ns = NodeShellNamespace()
	pod = fmt.Sprintf("node-shell-%s-%s", trimName(node, 40), rand.String(5))
	priv := true
	zero := int64(0)
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: pod, Namespace: ns, Labels: map[string]string{nodeShellLabel: "true"},
			Annotations: map[string]string{"k-vision/user": os.Getenv("USER")}},
		Spec: corev1.PodSpec{
			NodeName:                      node,
			HostPID:                       true,
			HostNetwork:                   true,
			RestartPolicy:                 corev1.RestartPolicyNever,
			TerminationGracePeriodSeconds: &zero,
			// Runs wherever the node is, including tainted system and GPU pools.
			Tolerations: []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
			Containers: []corev1.Container{{
				Name:            "shell",
				Image:           NodeShellImage(),
				Command:         []string{"sleep", "3600"}, // ends by itself if the session is lost
				Stdin:           true,
				TTY:             true,
				SecurityContext: &corev1.SecurityContext{Privileged: &priv},
				VolumeMounts:    []corev1.VolumeMount{{Name: "host", MountPath: "/host"}},
			}},
			Volumes: []corev1.Volume{{Name: "host", VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{Path: "/"}}}},
		},
	}
	if _, err = c.Typed.CoreV1().Pods(ns).Create(ctx, p, metav1.CreateOptions{}); err != nil {
		return ns, pod, err
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		got, gerr := c.Typed.CoreV1().Pods(ns).Get(ctx, pod, metav1.GetOptions{})
		if gerr == nil {
			switch got.Status.Phase {
			case corev1.PodRunning:
				return ns, pod, nil
			case corev1.PodFailed, corev1.PodSucceeded:
				c.StopNodeShell(ns, pod)
				return ns, pod, fmt.Errorf("node shell pod ended before it started (%s)", got.Status.Phase)
			}
			for _, cs := range got.Status.ContainerStatuses {
				if w := cs.State.Waiting; w != nil && (w.Reason == "ImagePullBackOff" || w.Reason == "ErrImagePull") {
					c.StopNodeShell(ns, pod)
					return ns, pod, fmt.Errorf("cannot pull %s on this node; set KV_NODE_SHELL_IMAGE to an image it can pull", NodeShellImage())
				}
			}
		}
		select {
		case <-ctx.Done():
			c.StopNodeShell(ns, pod)
			return ns, pod, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	c.StopNodeShell(ns, pod)
	return ns, pod, fmt.Errorf("node shell pod did not start within 90s")
}

// StopNodeShell deletes the helper pod. It uses its own context: it runs when the
// session's context is already gone.
func (c *Cluster) StopNodeShell(ns, pod string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	zero := int64(0)
	_ = c.Typed.CoreV1().Pods(ns).Delete(ctx, pod, metav1.DeleteOptions{GracePeriodSeconds: &zero})
}

// NodeShellCommand enters the host's namespaces through PID 1, so the shell sees the
// node's own processes, network and filesystem; it falls back to a chroot of /host.
var NodeShellCommand = []string{"nsenter", "-t", "1", "-m", "-u", "-i", "-n", "-p", "--",
	"sh", "-c", "if command -v bash >/dev/null 2>&1; then exec bash -l; else exec sh -l; fi"}

func trimName(s string, n int) string {
	s = strings.ToLower(s)
	if i := strings.IndexByte(s, '.'); i > 0 {
		s = s[:i]
	}
	if len(s) > n {
		s = s[:n]
	}
	return strings.Trim(s, "-")
}
