package kube

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

// Copying files in and out of containers works the way `kubectl cp` does: tar runs
// inside the container and the archive is streamed over exec. So the image needs
// `tar` (and `sh` for listing); distroless images have neither, and say so.
//
// For a node, the same is done in the node-shell helper pod with the host's
// filesystem mounted at /host -- the helper image (busybox) always has tar.

// FileEntry is one entry of a directory listing.
type FileEntry struct {
	Name    string    `json:"name"`
	Dir     bool      `json:"dir"`
	Link    string    `json:"link,omitempty"`
	Size    int64     `json:"size"`
	Mode    string    `json:"mode"`
	ModTime time.Time `json:"modTime"`
}

const (
	MaxDownload = 512 << 20 // per download, before compression
	MaxUpload   = 256 << 20 // per upload
)

// execRaw runs a command without a TTY, stdout and stderr kept apart.
func (c *Cluster) execRaw(ctx context.Context, namespace, pod, container string, command []string,
	stdin io.Reader, stdout, stderr io.Writer) error {
	req := c.Typed.CoreV1().RESTClient().Post().Resource("pods").Name(pod).Namespace(namespace).
		SubResource("exec").VersionedParams(&corev1.PodExecOptions{
		Container: container, Command: command, Stdin: stdin != nil, Stdout: true, Stderr: true, TTY: false,
	}, scheme.ParameterCodec)
	spdyExec, err := remotecommand.NewSPDYExecutor(c.Config, http.MethodPost, req.URL())
	if err != nil {
		return err
	}
	var exec remotecommand.Executor = spdyExec
	if wsExec, werr := remotecommand.NewWebSocketExecutor(c.Config, "GET", req.URL().String()); werr == nil {
		if fb, ferr := remotecommand.NewFallbackExecutor(wsExec, spdyExec, func(err error) bool {
			return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
		}); ferr == nil {
			exec = fb
		}
	}
	return exec.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: stdin, Stdout: stdout, Stderr: stderr})
}

func explainExec(err error, stderr string) error {
	msg := strings.TrimSpace(stderr)
	switch {
	case strings.Contains(msg, "not found") && (strings.Contains(msg, "tar") || strings.Contains(msg, "sh")),
		err != nil && strings.Contains(err.Error(), "executable file not found"):
		return errors.New("this container has no tar/sh (a distroless or scratch image), so files can't be copied; use a debug container")
	case msg != "":
		return errors.New(firstLine(msg))
	}
	return err
}

// ListDir lists one directory in a container: name, type, size, mode, time.
func (c *Cluster) ListDir(ctx context.Context, namespace, pod, container, dir string) ([]FileEntry, error) {
	dir = cleanPath(dir)
	// One stat line per entry, separated by a NUL so odd names survive. `ls -la` output
	// differs between busybox, GNU and BSD; stat and find do too, so this sticks to
	// what POSIX sh has: a loop and test.
	script := `cd -- "$1" 2>/dev/null || { echo "no such directory: $1" >&2; exit 2; }
for f in .* *; do
  [ "$f" = . ] || [ "$f" = .. ] && continue
  [ -e "$f" ] || [ -L "$f" ] || continue
  t=f; [ -d "$f" ] && t=d; [ -L "$f" ] && t=l
  s=$(wc -c < "$f" 2>/dev/null || echo 0); [ "$t" = d ] && s=0
  m=$(date -r "$f" +%s 2>/dev/null || echo 0)
  l=; [ "$t" = l ] && l=$(readlink "$f" 2>/dev/null)
  printf '%s\t%s\t%s\t%s\t%s\0' "$t" "$s" "$m" "$l" "$f"
done`
	var out, errb bytes.Buffer
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err := c.execRaw(ctx, namespace, pod, container, []string{"sh", "-c", script, "kv", dir}, nil, &out, &errb)
	if err != nil {
		return nil, explainExec(err, errb.String())
	}
	var list []FileEntry
	for _, rec := range strings.Split(out.String(), "\x00") {
		parts := strings.SplitN(rec, "\t", 5)
		if len(parts) != 5 {
			continue
		}
		size, _ := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		mt, _ := strconv.ParseInt(parts[2], 10, 64)
		e := FileEntry{Name: parts[4], Dir: parts[0] == "d", Link: parts[3], Size: size}
		if mt > 0 {
			e.ModTime = time.Unix(mt, 0)
		}
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Dir != list[j].Dir {
			return list[i].Dir
		}
		return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
	})
	return list, nil
}

// Download streams `p` (a file or a directory) out of a container as a tar archive.
// The caller turns a single file back into its own bytes (see SingleFile).
func (c *Cluster) Download(ctx context.Context, namespace, pod, container, p string, w io.Writer) (err error) {
	p = cleanPath(p)
	a := Audit{Context: c.Context, Action: "copy-from", Kind: "pods", Namespace: namespace, Name: pod,
		Detail: fmt.Sprintf("container=%s path=%s", container, p)}
	defer func() { record(a, err) }()
	dir, base := path.Split(p)
	if base == "" {
		base = "."
	}
	var errb bytes.Buffer
	lw := &limitWriter{w: w, n: MaxDownload}
	err = c.execRaw(ctx, namespace, pod, container, []string{"tar", "cf", "-", "-C", cleanPath(dir), base}, nil, lw, &errb)
	if lw.over {
		return fmt.Errorf("larger than %d MB; copy a smaller path", MaxDownload>>20)
	}
	if err != nil || (errb.Len() > 0 && lw.written == 0) {
		return explainExec(err, errb.String())
	}
	return nil
}

// Upload puts files into a directory of a container, from a tar archive.
func (c *Cluster) Upload(ctx context.Context, namespace, pod, container, dir string, archive io.Reader) (err error) {
	dir = cleanPath(dir)
	a := Audit{Context: c.Context, Action: "copy-to", Kind: "pods", Namespace: namespace, Name: pod,
		Detail: fmt.Sprintf("container=%s dir=%s", container, dir)}
	defer func() { record(a, err) }()
	var errb bytes.Buffer
	err = c.execRaw(ctx, namespace, pod, container, []string{"tar", "xf", "-", "-C", dir}, archive, io.Discard, &errb)
	if err != nil || errb.Len() > 0 {
		return explainExec(err, errb.String())
	}
	return nil
}

// TarOf packs uploaded files into a tar archive for Upload.
func TarOf(files map[string][]byte) (*bytes.Buffer, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		// Names are relative to the target directory; "../" would be cleaned into it
		// anyway, but a name that tries to climb out is a mistake worth refusing.
		if strings.Contains("/"+n+"/", "/../") || strings.HasPrefix(n, "/") {
			return nil, fmt.Errorf("bad file name %q: names must stay inside the target directory", n)
		}
		clean := path.Clean(n)
		if clean == "." || clean == "" {
			return nil, fmt.Errorf("bad file name %q", n)
		}
		data := files[n]
		if err := tw.WriteHeader(&tar.Header{Name: clean, Mode: 0o644, Size: int64(len(data)), ModTime: time.Now()}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(data); err != nil {
			return nil, err
		}
	}
	return &buf, tw.Close()
}

// SingleFile returns the content of a tar holding exactly one regular file, so a
// downloaded file arrives as itself rather than as an archive.
func SingleFile(archive []byte) (name string, data []byte, ok bool) {
	tr := tar.NewReader(bytes.NewReader(archive))
	h, err := tr.Next()
	if err != nil || h.Typeflag != tar.TypeReg {
		return "", nil, false
	}
	data, err = io.ReadAll(tr)
	if err != nil {
		return "", nil, false
	}
	if _, err := tr.Next(); err != io.EOF {
		return "", nil, false
	}
	return path.Base(h.Name), data, true
}

func cleanPath(p string) string {
	if p == "" {
		return "/"
	}
	return path.Clean("/" + p)
}

type limitWriter struct {
	w       io.Writer
	n       int64
	written int64
	over    bool
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.written+int64(len(p)) > l.n {
		l.over = true
		return 0, errors.New("size limit")
	}
	n, err := l.w.Write(p)
	l.written += int64(n)
	return n, err
}
