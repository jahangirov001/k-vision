package kube

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/transport/spdy"
)

// Containers lists a pod's containers, init containers first, for the log picker.
func (c *Cluster) Containers(ctx context.Context, namespace, pod string) ([]string, error) {
	p, err := c.Typed.CoreV1().Pods(namespace).Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, ct := range p.Spec.InitContainers {
		out = append(out, ct.Name)
	}
	for _, ct := range p.Spec.Containers {
		out = append(out, ct.Name)
	}
	return out, nil
}

// LogOptions are the knobs a log viewer needs.
type LogOptions struct {
	Container string
	Follow    bool
	Previous  bool  // the last crashed instance -- what you want for CrashLoopBackOff
	TailLines int64 // 0 = 500
	// Timestamps prefixes each line with the kubelet's RFC3339Nano time, which is
	// what lets lines from several pods be merged into one ordered view.
	Timestamps bool
}

// Logs streams a container's log line by line into `out` until ctx is cancelled or
// the stream ends. `out` is closed when it returns.
func (c *Cluster) Logs(ctx context.Context, namespace, pod string, o LogOptions, out chan<- string) error {
	defer close(out)
	tail := o.TailLines
	if tail == 0 {
		tail = 500
	}
	req := c.Typed.CoreV1().Pods(namespace).GetLogs(pod, &corev1.PodLogOptions{
		Container: o.Container, Follow: o.Follow, Previous: o.Previous, TailLines: &tail, Timestamps: o.Timestamps,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return err
	}
	defer stream.Close()
	sc := bufio.NewScanner(stream)
	sc.Buffer(make([]byte, 64*1024), 1024*1024) // JSON log lines can be long
	for sc.Scan() {
		select {
		case out <- sc.Text():
		case <-ctx.Done():
			return nil
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

// Exec runs a command in a container with a TTY, wired to the given streams. The
// command is tried in order -- bash, then sh -- because distroless images have neither
// and alpine has only sh.
func (c *Cluster) Exec(ctx context.Context, namespace, pod, container string, command []string,
	stdin io.Reader, stdout io.Writer, sizes remotecommand.TerminalSizeQueue) (err error) {
	a := Audit{Context: c.Context, Action: "exec", Kind: "pods", Namespace: namespace, Name: pod,
		Detail: fmt.Sprintf("container=%s cmd=%s", container, strings.Join(command, " "))}
	defer func() { record(a, err) }()
	req := c.Typed.CoreV1().RESTClient().Post().Resource("pods").Name(pod).Namespace(namespace).
		SubResource("exec").VersionedParams(&corev1.PodExecOptions{
		Container: container, Command: command, Stdin: true, Stdout: true, Stderr: false, TTY: true,
	}, scheme.ParameterCodec)
	// kubectl's own order: the WebSocket protocol first, SPDY when the server or a
	// proxy in between refuses the upgrade.
	spdyExec, err := remotecommand.NewSPDYExecutor(c.Config, http.MethodPost, req.URL())
	if err != nil {
		return err
	}
	var exec remotecommand.Executor = spdyExec
	if wsExec, werr := remotecommand.NewWebSocketExecutor(c.Config, "GET", req.URL().String()); werr == nil {
		exec, err = remotecommand.NewFallbackExecutor(wsExec, spdyExec, func(err error) bool {
			return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
		})
		if err != nil {
			return err
		}
	}
	return exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin: stdin, Stdout: stdout, Tty: true, TerminalSizeQueue: sizes,
	})
}

// Forward is a running port-forward.
type Forward struct {
	ID        string `json:"id"`
	Context   string `json:"context"`
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	Local     int    `json:"local"`
	Remote    int    `json:"remote"`
	stop      chan struct{}
}

var (
	forwardsMu sync.Mutex
	forwards   = map[string]*Forward{}
)

// PortForward forwards localhost:local to pod:remote until StopForward. local 0
// picks a free port; the chosen one is in the returned Forward.
func (c *Cluster) PortForward(namespace, pod string, local, remote int) (*Forward, error) {
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/portforward", namespace, pod)
	host, err := url.Parse(c.Config.Host)
	if err != nil {
		return nil, err
	}
	host.Path = strings.TrimRight(host.Path, "/") + path
	transport, upgrader, err := spdy.RoundTripperFor(c.Config)
	if err != nil {
		return nil, err
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, host)
	stop, ready := make(chan struct{}), make(chan struct{})
	pf, err := portforward.NewOnAddresses(dialer, []string{"127.0.0.1"},
		[]string{fmt.Sprintf("%d:%d", local, remote)}, stop, ready, io.Discard, io.Discard)
	if err != nil {
		return nil, err
	}
	errc := make(chan error, 1)
	go func() { errc <- pf.ForwardPorts() }()
	select {
	case <-ready:
	case err := <-errc:
		return nil, err
	}
	ports, err := pf.GetPorts()
	if err != nil || len(ports) == 0 {
		close(stop)
		return nil, fmt.Errorf("port-forward started but reported no port: %v", err)
	}
	f := &Forward{
		ID:      fmt.Sprintf("%s/%s/%s:%d", c.Context, namespace, pod, remote),
		Context: c.Context, Namespace: namespace, Pod: pod,
		Local: int(ports[0].Local), Remote: remote, stop: stop,
	}
	forwardsMu.Lock()
	forwards[f.ID] = f
	forwardsMu.Unlock()
	record(Audit{Context: c.Context, Action: "port-forward", Kind: "pods", Namespace: namespace, Name: pod,
		Detail: fmt.Sprintf("127.0.0.1:%d -> %d", f.Local, remote)}, nil)
	return f, nil
}

// Forwards lists the running port-forwards.
func Forwards() []Forward {
	forwardsMu.Lock()
	defer forwardsMu.Unlock()
	out := make([]Forward, 0, len(forwards))
	for _, f := range forwards {
		out = append(out, *f)
	}
	return out
}

// StopForward stops one port-forward.
func StopForward(id string) {
	forwardsMu.Lock()
	defer forwardsMu.Unlock()
	if f, ok := forwards[id]; ok {
		close(f.stop)
		delete(forwards, id)
	}
}
