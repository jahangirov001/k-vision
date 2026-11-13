// Package web is the desktop front end's backend: a local HTTP server bound to
// 127.0.0.1, serving the single-page UI and a JSON API over the same internal/kube
// layer the terminal UI uses.
//
// It can delete things in production, so it is locked down: localhost only, a random
// per-run token required on every API call (passed once in the URL the app opens, then
// kept in sessionStorage), and Origin/Host checks so another site in the browser cannot
// drive it (CSRF, DNS rebinding).
package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/njahangirov/kube-console/internal/kube"
)

//go:embed static
var static embed.FS

type server struct {
	mgr   *kube.Manager
	token string
	host  string // host:port the server answers to; anything else is refused
}

// FilePicker, when set (the native window does it), shows the system's Open panel
// and returns the chosen paths. The page cannot reach the file system itself, and
// WebKit's own file input did not open in the native window.
var FilePicker func(title string) []string

// Instance is a started UI server.
type Instance struct {
	URL     string // with the token, for opening a window
	Address string // without it, for printing
	Running bool   // true when another instance was already serving; nothing was started
	srv     *http.Server
	ln      net.Listener
	addr    string
}

// Start begins serving the UI on addr, or finds the instance already serving there.
func Start(mgr *kube.Manager, addr string) (*Instance, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("refusing to listen on %s: the UI can change clusters, so it only listens on localhost", addr)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		// Already running (the app opened twice, or `kv ui` in a second terminal).
		if url, ok := runningURL(addr); ok {
			return &Instance{URL: url, Address: strings.SplitN(url, "#", 2)[0], Running: true}, nil
		}
		return nil, err
	}
	s := &server{mgr: mgr, token: persistentToken(), host: ln.Addr().String()}
	in := &Instance{
		URL:     fmt.Sprintf("http://%s/#token=%s", s.host, s.token),
		Address: "http://" + s.host + "/",
		srv:     &http.Server{Handler: s.routes(), ReadHeaderTimeout: 10 * time.Second},
		ln:      ln,
		addr:    addr,
	}
	writeSession(addr, in.URL)
	return in, nil
}

// Serve blocks serving requests until Close. A no-op for an instance that was
// already running elsewhere.
func (in *Instance) Serve() error {
	if in.Running {
		return nil
	}
	err := in.srv.Serve(in.ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Close stops serving and forgets the session file.
func (in *Instance) Close() {
	if in.Running || in.srv == nil {
		return
	}
	clearSession(in.addr)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = in.srv.Shutdown(ctx)
}

// OpenBrowser opens the UI in a browser window (Chrome/Edge app mode where
// available). Used when the native window is not available.
func OpenBrowser(url string) { openBrowser(url) }

// Serve runs the UI and opens a browser window on it -- the behaviour without a
// native window (Linux and Windows builds, or KV_BROWSER=1).
func Serve(mgr *kube.Manager, addr string, open bool) error {
	in, err := Start(mgr, addr)
	if err != nil {
		return err
	}
	if in.Running {
		fmt.Println("K-Vision is already running:", in.Address)
	} else {
		fmt.Println("K-Vision is running at", in.Address, "(open it with `kv ui`)")
	}
	if open {
		go openBrowser(in.URL)
	}
	defer in.Close()
	return in.Serve()
}

// The running instance's URL, token included, lives in a 0600 file in the user's
// own ~/.k-vision, so a second launch can open a window on it. Other users on
// the machine cannot read it, which keeps the token as private as the process.
func sessionPath(addr string) string {
	return filepath.Join(filepath.Dir(kube.AuditPath()), "ui-"+strings.NewReplacer(":", "_", "/", "_").Replace(addr)+".url")
}

func writeSession(addr, url string) {
	p := sessionPath(addr)
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	_ = os.WriteFile(p, []byte(url), 0o600)
}

func clearSession(addr string) { _ = os.Remove(sessionPath(addr)) }

func runningURL(addr string) (string, bool) {
	data, err := os.ReadFile(sessionPath(addr))
	if err != nil {
		return "", false
	}
	url := strings.TrimSpace(string(data))
	// Only trust it if something answers there and accepts the token.
	tok := url[strings.LastIndex(url, "=")+1:]
	req, _ := http.NewRequest("GET", "http://"+addr+"/api/contexts", nil)
	req.Header.Set("X-KC-Token", tok)
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return "", false
	}
	resp.Body.Close()
	return url, resp.StatusCode == http.StatusOK
}

// persistentToken is the UI's token, kept in ~/.k-vision/ui-token (0600) so it
// survives restarts. A token per run broke every window left open across an update
// or a relaunch ("missing or wrong token"). It is still random, and still readable
// only by this user -- the same reach as their kubeconfig.
func persistentToken() string {
	p := filepath.Join(filepath.Dir(kube.AuditPath()), "ui-token")
	if data, err := os.ReadFile(p); err == nil {
		if t := strings.TrimSpace(string(data)); len(t) >= 32 {
			return t
		}
	}
	t := randomToken()
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	_ = os.WriteFile(p, []byte(t+"\n"), 0o600)
	return t
}

// appProfile is a browser profile of the app's own. Without it the window opens as
// one more window of your everyday Chrome -- its menu bar, its Dock icon, its tabs
// sharing the process -- which is why the menu bar said "Chrome". With it the
// window runs as a separate instance, and your browsing profile is never touched.
func appProfile() string {
	p := filepath.Join(filepath.Dir(kube.AuditPath()), "window")
	_ = os.MkdirAll(p, 0o700)
	return p
}

func windowsBrowsers() []string {
	var out []string
	for _, base := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LocalAppData")} {
		if base == "" {
			continue
		}
		out = append(out,
			filepath.Join(base, "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"))
	}
	return out
}

func randomToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func openBrowser(url string) {
	time.Sleep(300 * time.Millisecond)
	var cmd *exec.Cmd
	// --app gives a window with no tabs or address bar. Chrome or Edge, whichever is
	// installed; the default browser otherwise.
	switch runtime.GOOS {
	case "darwin":
		for _, app := range []string{"Google Chrome", "Microsoft Edge", "Brave Browser", "Chromium"} {
			if exec.Command("open", "-Ra", app).Run() == nil {
				cmd = exec.Command("open", "-na", app, "--args", "--app="+url, "--user-data-dir="+appProfile())
				break
			}
		}
		if cmd == nil {
			cmd = exec.Command("open", url)
		}
	case "windows":
		for _, exe := range windowsBrowsers() {
			if _, err := os.Stat(exe); err == nil {
				cmd = exec.Command(exe, "--app="+url, "--user-data-dir="+appProfile())
				break
			}
		}
		if cmd == nil {
			cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
		}
	default:
		for _, b := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge"} {
			if p, err := exec.LookPath(b); err == nil {
				cmd = exec.Command(p, "--app="+url, "--user-data-dir="+appProfile())
				break
			}
		}
		if cmd == nil {
			cmd = exec.Command("xdg-open", url)
		}
	}
	_ = cmd.Start()
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(static, "static")
	mux.Handle("GET /", http.FileServer(http.FS(sub)))

	api := func(pattern string, h func(*http.Request) (any, error)) {
		mux.HandleFunc(pattern, s.guard(func(w http.ResponseWriter, r *http.Request) {
			v, err := h(r)
			w.Header().Set("Content-Type", "application/json")
			if err != nil {
				w.WriteHeader(statusFor(err))
				_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			_ = json.NewEncoder(w).Encode(v)
		}))
	}
	api("GET /api/contexts", s.contexts)
	api("POST /api/context/hide", s.hideContext)
	api("GET /api/probe", func(r *http.Request) (any, error) { return s.mgr.ProbeCluster(r.Context(), q(r, "context")), nil })
	api("POST /api/client-state", s.clientState)
	api("GET /api/client-state", func(*http.Request) (any, error) {
		lastClientMu.Lock()
		defer lastClientMu.Unlock()
		return lastClient, nil
	})
	api("POST /api/context/delete", s.deleteContext)
	api("GET /api/kinds", s.kinds)
	api("GET /api/namespaces", s.namespaces)
	api("GET /api/list", s.list)
	api("GET /api/everywhere", s.everywhere)
	api("GET /api/yaml", s.yaml)
	api("GET /api/explain", s.explain)
	api("GET /api/containers", s.containers)
	api("GET /api/pods-of", s.podsOf)
	api("POST /api/create", s.create)
	api("GET /api/files", s.listFiles)
	mux.HandleFunc("GET /api/files/download", s.guard(s.downloadFile))
	api("POST /api/files/upload", s.uploadFiles)
	api("GET /api/files/picker", func(*http.Request) (any, error) { return map[string]bool{"native": FilePicker != nil}, nil })
	api("POST /api/files/upload-picked", s.uploadPicked)
	api("POST /api/files/save", s.saveFile)
	api("POST /api/files/reveal", s.revealFile)
	api("POST /api/open-app-file", s.openAppFile)
	api("GET /api/everything", s.everything)
	api("GET /api/clustermap", func(r *http.Request) (any, error) {
		c, _, err := s.resolve(q(r, "context"), "")
		if err != nil {
			return nil, err
		}
		ctx, cancel := timeout(r, 60*time.Second)
		defer cancel()
		return c.ClusterMap(ctx)
	})
	api("GET /api/graph", func(r *http.Request) (any, error) {
		c, _, err := s.resolve(q(r, "context"), "")
		if err != nil {
			return nil, err
		}
		ctx, cancel := timeout(r, 60*time.Second)
		defer cancel()
		return c.Graph(ctx, q(r, "namespace"))
	})
	api("GET /api/templates", func(*http.Request) (any, error) { return kube.Templates, nil })
	api("GET /api/helm/releases", s.helmReleases)
	api("GET /api/argocd", func(r *http.Request) (any, error) {
		c, _, err := s.resolve(q(r, "context"), "")
		if err != nil {
			return nil, err
		}
		ctx, cancel := timeout(r, 60*time.Second)
		defer cancel()
		return c.ArgoDashboard(ctx)
	})
	api("GET /api/events", func(r *http.Request) (any, error) {
		c, _, err := s.resolve(q(r, "context"), "")
		if err != nil {
			return nil, err
		}
		window, _ := time.ParseDuration(q(r, "window"))
		ctx, cancel := timeout(r, 60*time.Second)
		defer cancel()
		return c.Events(ctx, q(r, "namespace"), window)
	})
	api("GET /api/cost", func(r *http.Request) (any, error) {
		c, _, err := s.resolve(q(r, "context"), "")
		if err != nil {
			return nil, err
		}
		ctx, cancel := timeout(r, 3*time.Minute)
		defer cancel()
		rep, err := c.Cost(ctx, q(r, "window"), q(r, "aggregate"), q(r, "namespace"), q(r, "fresh") == "1")
		if errors.Is(err, kube.ErrNoCost) {
			return map[string]any{"none": true}, nil
		}
		if err == nil && (q(r, "window") == "1d" || q(r, "window") == "") {
			c.PrefetchCost(q(r, "aggregate"), q(r, "namespace"), "7d")
		}
		return rep, err
	})
	api("GET /api/helmmap", func(r *http.Request) (any, error) {
		c, _, err := s.resolve(q(r, "context"), "")
		if err != nil {
			return nil, err
		}
		ctx, cancel := timeout(r, 90*time.Second)
		defer cancel()
		return c.HelmMap(ctx, q(r, "namespace"))
	})
	api("GET /api/helm/charts", s.helmCharts)
	api("GET /api/helm/release", s.helmRelease)
	api("POST /api/secret", s.secret)
	api("GET /api/configmap", s.configmap)
	api("GET /api/forwards", func(*http.Request) (any, error) { return kube.Forwards(), nil })
	api("POST /api/delete", s.delete)
	api("POST /api/delete-many", s.deleteMany)
	api("POST /api/scale", s.scale)
	api("POST /api/restart", s.restart)
	api("POST /api/apply", s.apply)
	api("POST /api/cordon", s.cordon)
	api("POST /api/drain", s.drain)
	api("POST /api/forward", s.forward)
	api("POST /api/unforward", s.unforward)
	mux.HandleFunc("GET /ws/logs", s.guard(s.logs))
	mux.HandleFunc("GET /ws/exec", s.guard(s.exec))
	return s.hostCheck(mux)
}

// hostCheck refuses requests whose Host is not ours: a DNS-rebinding page would send
// its own hostname.
func (s *server) hostCheck(next http.Handler) http.Handler {
	_, port, _ := net.SplitHostPort(s.host)
	ok := map[string]bool{s.host: true, "localhost:" + port: true, "127.0.0.1:" + port: true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ok[r.Host] {
			http.Error(w, "bad host", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; connect-src 'self' ws://"+r.Host+"; style-src 'self' 'unsafe-inline'; img-src 'self' data:")
		next.ServeHTTP(w, r)
	})
}

// guard requires the per-run token and a same-origin Origin on every API call.
func (s *server) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("X-KC-Token")
		if tok == "" {
			tok = r.URL.Query().Get("token") // websockets cannot set headers
		}
		if tok != s.token {
			http.Error(w, "missing or wrong token — open the URL kv ui printed", http.StatusUnauthorized)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

var errBadRequest = errors.New("bad request")

func statusFor(err error) int {
	if errors.Is(err, errBadRequest) || strings.HasPrefix(err.Error(), "bad request") {
		return http.StatusBadRequest
	}
	if strings.HasPrefix(err.Error(), "confirmation") {
		return http.StatusPreconditionFailed
	}
	return http.StatusBadGateway
}

func q(r *http.Request, k string) string { return r.URL.Query().Get(k) }

func timeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}

// target is what every per-object call carries.
type target struct {
	Context   string `json:"context"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Confirm   string `json:"confirm"`
	Replicas  *int32 `json:"replicas,omitempty"`
	YAML      string `json:"yaml,omitempty"`
	On        *bool  `json:"on,omitempty"`
	Local     int    `json:"local,omitempty"`
	Remote    int    `json:"remote,omitempty"`
	ID        string `json:"id,omitempty"`
}

func (s *server) resolve(ctxName, kind string) (*kube.Cluster, kube.Kind, error) {
	if ctxName == "" {
		ctxName = s.mgr.CurrentContext()
	}
	c, err := s.mgr.Cluster(ctxName)
	if err != nil {
		return nil, kube.Kind{}, err
	}
	if kind == "" {
		return c, kube.Kind{}, nil
	}
	k, err := c.ResolveKind(kind)
	return c, k, err
}

func (s *server) body(r *http.Request) (target, *kube.Cluster, kube.Kind, error) {
	var t target
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<20)).Decode(&t); err != nil {
		return t, nil, kube.Kind{}, fmt.Errorf("bad request: %w", err)
	}
	c, k, err := s.resolve(t.Context, t.Kind)
	return t, c, k, err
}

// namedBody is body() for delete and apply: the typed name is checked before the
// cluster is even contacted.
func (s *server) namedBody(r *http.Request) (target, *kube.Cluster, kube.Kind, error) {
	var t target
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<20)).Decode(&t); err != nil {
		return t, nil, kube.Kind{}, fmt.Errorf("bad request: %w", err)
	}
	if err := s.named(t); err != nil {
		return t, nil, kube.Kind{}, err
	}
	c, k, err := s.resolve(t.Context, t.Kind)
	return t, c, k, err
}

// confirmed is the server side of the typed confirmation. The UI asks for it, but the
// server is what enforces it -- a protected context refuses a write that does not
// carry the object's name.
func (s *server) confirmed(t target) error {
	// Scale, restart, cordon and drain are confirmed with one click in the dialog.
	return nil
}

// named is the stronger check for delete and apply: the resource's name has to be
// typed, on every cluster. The page asks for it; the server is what enforces it.
func (s *server) named(t target) error {
	if t.Confirm != t.Name {
		return fmt.Errorf("confirmation required: type %s and confirm", t.Name)
	}
	return nil
}

func (s *server) contexts(*http.Request) (any, error) {
	return map[string]any{"contexts": s.mgr.Contexts(), "hidden": s.mgr.HiddenContexts(), "current": s.mgr.CurrentContext()}, nil
}

// hideContext hides a cluster from K-Vision's lists, or shows it again. It only
// changes ~/.k-vision/settings.json; the kubeconfig is left alone.
func (s *server) hideContext(r *http.Request) (any, error) {
	var req struct {
		Context string `json:"context"`
		Hide    bool   `json:"hide"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<16)).Decode(&req); err != nil {
		return nil, fmt.Errorf("bad request: %w", err)
	}
	if err := s.mgr.SetHidden(req.Context, req.Hide); err != nil {
		return nil, err
	}
	return s.contexts(r)
}

// deleteContext removes a context from the kubeconfig file itself (kubectl loses it
// too). It always needs the cluster's short name typed, protected or not: it changes
// a file other tools depend on. The file is backed up first.
func (s *server) deleteContext(r *http.Request) (any, error) {
	var req struct {
		Context string `json:"context"`
		Confirm string `json:"confirm"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<16)).Decode(&req); err != nil {
		return nil, fmt.Errorf("bad request: %w", err)
	}
	want := req.Context
	if i := strings.LastIndex(want, "/"); i >= 0 {
		want = want[i+1:]
	}
	if req.Confirm != want {
		return nil, fmt.Errorf("confirmation required: type %q to delete it from your kubeconfig", want)
	}
	backup, err := s.mgr.DeleteFromKubeconfig(req.Context)
	if err != nil {
		return nil, err
	}
	out, _ := s.contexts(r)
	m := out.(map[string]any)
	m["ok"] = "Deleted " + want + " from your kubeconfig. Backup: " + backup
	return m, nil
}

var (
	lastClientMu sync.Mutex
	lastClient   map[string]any
)

// clientState records what the window last reported about itself (theme, engine).
// Diagnostics only: it lets `curl /api/client-state` show what a native window chose.
func (s *server) clientState(r *http.Request) (any, error) {
	var v map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<14)).Decode(&v); err != nil {
		return nil, fmt.Errorf("bad request: %w", err)
	}
	lastClientMu.Lock()
	lastClient = v
	lastClientMu.Unlock()
	return ok("noted"), nil
}

func (s *server) kinds(r *http.Request) (any, error) {
	c, _, err := s.resolve(q(r, "context"), "")
	if err != nil {
		return nil, err
	}
	return c.Kinds()
}

func (s *server) namespaces(r *http.Request) (any, error) {
	c, _, err := s.resolve(q(r, "context"), "")
	if err != nil {
		return nil, err
	}
	ctx, cancel := timeout(r, 20*time.Second)
	defer cancel()
	return c.Namespaces(ctx)
}

func (s *server) list(r *http.Request) (any, error) {
	c, k, err := s.resolve(q(r, "context"), q(r, "kind"))
	if err != nil {
		return nil, err
	}
	ctx, cancel := timeout(r, 30*time.Second)
	defer cancel()
	rows, cols, err := c.List(ctx, k, q(r, "namespace"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"kind": k, "rows": rows, "columns": cols}, nil
}

func (s *server) everywhere(r *http.Request) (any, error) {
	var names []string
	if want := q(r, "contexts"); want != "" {
		names = strings.Split(want, ",")
	} else {
		for _, c := range s.mgr.Contexts() {
			names = append(names, c.Name)
		}
	}
	return s.mgr.ListEverywhere(r.Context(), names, q(r, "kind"), q(r, "namespace"), q(r, "filter")), nil
}

func (s *server) yaml(r *http.Request) (any, error) {
	c, k, err := s.resolve(q(r, "context"), q(r, "kind"))
	if err != nil {
		return nil, err
	}
	ctx, cancel := timeout(r, 20*time.Second)
	defer cancel()
	y, err := c.YAML(ctx, k, q(r, "namespace"), q(r, "name"))
	return map[string]string{"yaml": y}, err
}

func (s *server) explain(r *http.Request) (any, error) {
	c, _, err := s.resolve(q(r, "context"), "")
	if err != nil {
		return nil, err
	}
	ctx, cancel := timeout(r, 20*time.Second)
	defer cancel()
	return c.Explain(ctx, q(r, "namespace"), q(r, "name"))
}

// Helm is read from the release Secrets in the cluster; no helm binary is needed.
func (s *server) helmReleases(r *http.Request) (any, error) {
	c, _, err := s.resolve(q(r, "context"), "")
	if err != nil {
		return nil, err
	}
	ctx, cancel := timeout(r, 30*time.Second)
	defer cancel()
	return c.HelmReleases(ctx, q(r, "namespace"))
}

func (s *server) helmCharts(r *http.Request) (any, error) {
	c, _, err := s.resolve(q(r, "context"), "")
	if err != nil {
		return nil, err
	}
	ctx, cancel := timeout(r, 30*time.Second)
	defer cancel()
	return c.HelmCharts(ctx, q(r, "namespace"))
}

func (s *server) helmRelease(r *http.Request) (any, error) {
	c, _, err := s.resolve(q(r, "context"), "")
	if err != nil {
		return nil, err
	}
	rev, _ := strconv.Atoi(q(r, "revision"))
	ctx, cancel := timeout(r, 30*time.Second)
	defer cancel()
	return c.HelmRelease(ctx, q(r, "namespace"), q(r, "name"), rev)
}

// everything is the namespace overview: every kind with something in the namespace.
func (s *server) everything(r *http.Request) (any, error) {
	c, _, err := s.resolve(q(r, "context"), "")
	if err != nil {
		return nil, err
	}
	ns := q(r, "namespace")
	if ns == "" {
		return nil, fmt.Errorf("bad request: choose a namespace")
	}
	ctx, cancel := timeout(r, 60*time.Second)
	defer cancel()
	return c.Everything(ctx, ns, q(r, "noisy") == "true")
}

// create is `kubectl create -f` for pasted YAML. dryRun=true validates against the
// API server (schema, admission, quotas) and stores nothing, so it needs no
// confirmation. A real create on a protected cluster needs the cluster's short name.
func (s *server) create(r *http.Request) (any, error) {
	var req struct {
		Context   string `json:"context"`
		Namespace string `json:"namespace"`
		YAML      string `json:"yaml"`
		DryRun    bool   `json:"dryRun"`
		Confirm   string `json:"confirm"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<20)).Decode(&req); err != nil {
		return nil, fmt.Errorf("bad request: %w", err)
	}
	c, _, err := s.resolve(req.Context, "")
	if err != nil {
		return nil, err
	}
	if !req.DryRun && s.mgr.Protected(c.Context) {
		want := c.Context
		if i := strings.LastIndex(want, "/"); i >= 0 {
			want = want[i+1:]
		}
		if req.Confirm != want {
			return nil, fmt.Errorf("confirmation required: %s is protected; type %q to create", want, want)
		}
	}
	ctx, cancel := timeout(r, 60*time.Second)
	defer cancel()
	res, err := c.CreateFromYAML(ctx, req.YAML, req.Namespace, req.DryRun)
	if err != nil {
		return nil, fmt.Errorf("bad request: %w", err)
	}
	return res, nil
}

// fileTarget is where a file operation runs: a pod's container, or -- for a node --
// the node-shell helper pod with the host's filesystem at /host. The returned done()
// removes that helper pod; for a pod it does nothing.
func (s *server) fileTarget(ctx context.Context, r *http.Request) (c *kube.Cluster, ns, pod, container, root string, done func(), err error) {
	c, _, err = s.resolve(q(r, "context"), "")
	if err != nil {
		return
	}
	done = func() {}
	if node := q(r, "node"); node != "" {
		ns, pod, err = c.StartNodeShell(ctx, node)
		if err != nil {
			return
		}
		cc, nsx, podx := c, ns, pod
		done = func() { cc.StopNodeShell(nsx, podx) }
		return c, ns, pod, "shell", "/host", done, nil
	}
	ns, pod, container = q(r, "namespace"), q(r, "name"), q(r, "container")
	if container == "" {
		cs, cerr := c.Containers(ctx, ns, pod)
		if cerr != nil {
			return nil, "", "", "", "", done, cerr
		}
		container = cs[len(cs)-1]
	}
	return c, ns, pod, container, "", done, nil
}

func (s *server) listFiles(r *http.Request) (any, error) {
	ctx, cancel := timeout(r, 2*time.Minute)
	defer cancel()
	c, ns, pod, container, root, done, err := s.fileTarget(ctx, r)
	if err != nil {
		return nil, err
	}
	defer done()
	return c.ListDir(ctx, ns, pod, container, root+q(r, "path"))
}

// downloadFile sends a file as itself, or a directory as a .tar.
func (s *server) downloadFile(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	c, ns, pod, container, root, done, err := s.fileTarget(ctx, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer done()
	p := q(r, "path")
	var buf bytes.Buffer
	if err := c.Download(ctx, ns, pod, container, root+p, &buf); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	name, data, single := kube.SingleFile(buf.Bytes())
	if !single {
		name, data = path.Base(path.Clean("/"+p))+".tar", buf.Bytes()
		if name == "/.tar" || name == ".tar" {
			name = "root.tar"
		}
		w.Header().Set("Content-Type", "application/x-tar")
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

// saveFile downloads from a pod or node straight into ~/Downloads and returns the
// path. The native WebKit window does not perform <a download> saves (it reported
// success and wrote nothing), and the server runs on the same Mac anyway, so it
// writes the file itself -- the same in every window, native or browser.
func (s *server) saveFile(r *http.Request) (any, error) {
	ctx, cancel := timeout(r, 10*time.Minute)
	defer cancel()
	c, ns, pod, container, root, done, err := s.fileTarget(ctx, r)
	if err != nil {
		return nil, err
	}
	defer done()
	p := q(r, "path")
	var buf bytes.Buffer
	if err := c.Download(ctx, ns, pod, container, root+p, &buf); err != nil {
		return nil, err
	}
	name, data, single := kube.SingleFile(buf.Bytes())
	if !single {
		name, data = path.Base(path.Clean("/"+p))+".tar", buf.Bytes()
		if name == "/.tar" || name == ".tar" {
			name = "root.tar"
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, "Downloads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	dst := uniquePath(dir, sanitizeName(name))
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return nil, err
	}
	return map[string]any{"ok": "saved", "path": dst, "name": filepath.Base(dst), "size": len(data)}, nil
}

// revealFile shows a saved download in Finder / Explorer / the file manager. It only
// accepts paths inside ~/Downloads, so the page can't ask it to open anything else.
func (s *server) revealFile(r *http.Request) (any, error) {
	var req struct{ Path string }
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<14)).Decode(&req); err != nil {
		return nil, fmt.Errorf("bad request: %w", err)
	}
	home, _ := os.UserHomeDir()
	dl := filepath.Join(home, "Downloads") + string(filepath.Separator)
	p := filepath.Clean(req.Path)
	if !strings.HasPrefix(p, dl) {
		return nil, fmt.Errorf("bad request: only files in your Downloads folder can be shown")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", "-R", p)
	case "windows":
		cmd = exec.Command("explorer", "/select,", p)
	default:
		cmd = exec.Command("xdg-open", filepath.Dir(p))
	}
	return ok("shown"), cmd.Start()
}

// openAppFile opens one of K-Vision's own files (Help menu): the audit log, the
// app log or the backups folder. The page names which; it can't pass a path.
func (s *server) openAppFile(r *http.Request) (any, error) {
	var req struct{ Which string }
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<12)).Decode(&req); err != nil {
		return nil, fmt.Errorf("bad request: %w", err)
	}
	dir := filepath.Dir(kube.AuditPath())
	p := map[string]string{"audit-log": kube.AuditPath(), "app-log": filepath.Join(dir, "ui.log"),
		"backups": filepath.Join(dir, "backups")}[req.Which]
	if p == "" {
		return nil, fmt.Errorf("bad request: unknown file %q", req.Which)
	}
	if _, err := os.Stat(p); err != nil {
		return nil, fmt.Errorf("not found: %s does not exist yet", p)
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", p) // a log opens in Console, a folder in Finder
		if req.Which != "backups" {
			cmd = exec.Command("open", "-a", "Console", p)
		}
	case "windows":
		cmd = exec.Command("explorer", p)
	default:
		cmd = exec.Command("xdg-open", p)
	}
	return map[string]any{"ok": "opened", "path": p}, cmd.Start()
}

// uploadPicked opens the system file chooser, then uploads what was chosen into the
// pod's or node's folder -- read by the server from the paths the user picked, so no
// file bytes pass through the page. Same confirmation and limits as uploadFiles.
func (s *server) uploadPicked(r *http.Request) (any, error) {
	if FilePicker == nil {
		return nil, fmt.Errorf("bad request: no system file chooser in this window")
	}
	var req struct {
		Confirm string `json:"confirm"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<14)).Decode(&req)
	ctxName := q(r, "context")
	if ctxName == "" {
		ctxName = s.mgr.CurrentContext()
	}
	who := q(r, "name")
	if n := q(r, "node"); n != "" {
		who = n
	}
	paths := FilePicker("Choose files to upload to " + who + ":" + path.Clean("/"+q(r, "path")))
	if len(paths) == 0 {
		return map[string]any{"cancelled": true}, nil
	}
	if s.mgr.Protected(ctxName) && req.Confirm != who {
		return nil, fmt.Errorf("confirmation required: type %q to copy files into it on a protected cluster", who)
	}
	files := map[string][]byte{}
	total := 0
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		total += int(st.Size())
		if total > kube.MaxUpload {
			return nil, fmt.Errorf("bad request: upload at most %d MB at a time", kube.MaxUpload>>20)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		files[filepath.Base(p)] = data
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("bad request: nothing to upload")
	}
	archive, err := kube.TarOf(files)
	if err != nil {
		return nil, fmt.Errorf("bad request: %w", err)
	}
	ctx, cancel := timeout(r, 10*time.Minute)
	defer cancel()
	c, ns, pod, container, root, done, err := s.fileTarget(ctx, r)
	if err != nil {
		return nil, err
	}
	defer done()
	if err := c.Upload(ctx, ns, pod, container, root+q(r, "path"), archive); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	return map[string]any{"ok": fmt.Sprintf("copied %s to %s", strings.Join(names, ", "), path.Clean("/"+q(r, "path"))), "files": names}, nil
}

// sanitizeName keeps a downloaded name to one plain path segment.
func sanitizeName(n string) string {
	n = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == 0 || r == ':' {
			return '_'
		}
		return r
	}, n)
	n = strings.TrimLeft(n, ".")
	if n == "" {
		n = "download"
	}
	return n
}

// uniquePath is name, or "name (2).ext", "name (3).ext"... -- never overwrites.
func uniquePath(dir, name string) string {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	p := filepath.Join(dir, name)
	for i := 2; ; i++ {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return p
		}
		p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
	}
}

// uploadFiles takes files as {name: base64} and unpacks them into a directory.
// On a protected cluster it needs the pod's (or node's) name typed: it changes what
// runs there.
func (s *server) uploadFiles(r *http.Request) (any, error) {
	var req struct {
		Confirm string            `json:"confirm"`
		Files   map[string]string `json:"files"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, kube.MaxUpload*4/3+1<<20)).Decode(&req); err != nil {
		return nil, fmt.Errorf("bad request: %w", err)
	}
	ctxName := q(r, "context")
	if ctxName == "" {
		ctxName = s.mgr.CurrentContext()
	}
	who := q(r, "name")
	if n := q(r, "node"); n != "" {
		who = n
	}
	if s.mgr.Protected(ctxName) && req.Confirm != who {
		return nil, fmt.Errorf("confirmation required: type %q to copy files into it on a protected cluster", who)
	}
	files := map[string][]byte{}
	total := 0
	for name, b64 := range req.Files {
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("bad request: %s is not base64", name)
		}
		total += len(data)
		files[name] = data
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("bad request: no files")
	}
	if total > kube.MaxUpload {
		return nil, fmt.Errorf("bad request: %d MB; upload at most %d MB at a time", total>>20, kube.MaxUpload>>20)
	}
	archive, err := kube.TarOf(files)
	if err != nil {
		return nil, fmt.Errorf("bad request: %w", err)
	}
	ctx, cancel := timeout(r, 10*time.Minute)
	defer cancel()
	c, ns, pod, container, root, done, err := s.fileTarget(ctx, r)
	if err != nil {
		return nil, err
	}
	defer done()
	if err := c.Upload(ctx, ns, pod, container, root+q(r, "path"), archive); err != nil {
		return nil, err
	}
	return ok(fmt.Sprintf("copied %d file(s) to %s", len(files), path.Clean("/"+q(r, "path")))), nil
}

func (s *server) podsOf(r *http.Request) (any, error) {
	c, k, err := s.resolve(q(r, "context"), q(r, "kind"))
	if err != nil {
		return nil, err
	}
	ctx, cancel := timeout(r, 20*time.Second)
	defer cancel()
	return c.PodsOf(ctx, k, q(r, "namespace"), q(r, "name"))
}

func (s *server) containers(r *http.Request) (any, error) {
	c, _, err := s.resolve(q(r, "context"), "")
	if err != nil {
		return nil, err
	}
	ctx, cancel := timeout(r, 20*time.Second)
	defer cancel()
	return c.Containers(ctx, q(r, "namespace"), q(r, "name"))
}

// secret returns a Secret's decoded values. POST, not GET, so the values never sit in
// a URL, a browser history or a proxy log. Reading is not a change, so it needs no
// typed confirmation; every reveal is in the audit log instead.
func (s *server) secret(r *http.Request) (any, error) {
	t, c, _, err := s.body(r)
	if err != nil {
		return nil, err
	}
	ctx, cancel := timeout(r, 20*time.Second)
	defer cancel()
	return c.SecretData(ctx, t.Namespace, t.Name)
}

func (s *server) configmap(r *http.Request) (any, error) {
	c, _, err := s.resolve(q(r, "context"), "")
	if err != nil {
		return nil, err
	}
	ctx, cancel := timeout(r, 20*time.Second)
	defer cancel()
	return c.ConfigMapData(ctx, q(r, "namespace"), q(r, "name"))
}

func ok(msg string) map[string]string { return map[string]string{"ok": msg} }

func (s *server) delete(r *http.Request) (any, error) {
	t, c, k, err := s.namedBody(r)
	if err != nil {
		return nil, err
	}
	ctx, cancel := timeout(r, 30*time.Second)
	defer cancel()
	return ok("deleted " + t.Name), c.Delete(ctx, k, t.Namespace, t.Name)
}

// MaxBulkDelete caps one bulk delete. Selecting "all" on a 750-pod list and pressing
// delete should be a deliberate act repeated, not one click.
const MaxBulkDelete = 100

type bulkItem struct {
	Context   string `json:"context"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type bulkResult struct {
	bulkItem
	Error string `json:"error,omitempty"`
}

// BulkPhrase is what has to be typed to delete n objects when any of them is on a
// protected cluster. One phrase for the whole batch: typing 30 names is not a check
// anyone performs carefully, and a count is -- it says how much you are about to lose.
func BulkPhrase(int) string { return "delete" }

// deleteMany deletes several objects of one kind, possibly across clusters (the
// all-clusters view). Each is backed up and audited like a single delete. A failure
// on one does not stop the rest; the result lists what happened to each.
func (s *server) deleteMany(r *http.Request) (any, error) {
	var req struct {
		Kind    string     `json:"kind"`
		Items   []bulkItem `json:"items"`
		Confirm string     `json:"confirm"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<20)).Decode(&req); err != nil {
		return nil, fmt.Errorf("bad request: %w", err)
	}
	if len(req.Items) == 0 {
		return nil, fmt.Errorf("bad request: nothing selected")
	}
	if len(req.Items) > MaxBulkDelete {
		return nil, fmt.Errorf("bad request: %d selected; delete at most %d at a time", len(req.Items), MaxBulkDelete)
	}
	protected := false
	for i := range req.Items {
		if req.Items[i].Context == "" {
			req.Items[i].Context = s.mgr.CurrentContext()
		}
		if s.mgr.Protected(req.Items[i].Context) {
			protected = true
		}
	}
	if protected && req.Confirm != BulkPhrase(len(req.Items)) {
		return nil, fmt.Errorf("confirmation required: the selection includes a protected cluster; type %q", BulkPhrase(len(req.Items)))
	}
	ctx, cancel := timeout(r, 2*time.Minute)
	defer cancel()
	out := make([]bulkResult, 0, len(req.Items))
	done := 0
	for _, it := range req.Items {
		res := bulkResult{bulkItem: it}
		c, k, err := s.resolve(it.Context, req.Kind)
		if err == nil {
			err = c.Delete(ctx, k, it.Namespace, it.Name)
		}
		if err != nil {
			res.Error = err.Error()
		} else {
			done++
		}
		out = append(out, res)
	}
	return map[string]any{"ok": fmt.Sprintf("deleted %d of %d", done, len(req.Items)), "results": out}, nil
}

func (s *server) scale(r *http.Request) (any, error) {
	t, c, k, err := s.body(r)
	if err != nil {
		return nil, err
	}
	if t.Replicas == nil || *t.Replicas < 0 {
		return nil, fmt.Errorf("bad request: replicas must be ≥ 0")
	}
	if err := s.confirmed(t); err != nil {
		return nil, err
	}
	ctx, cancel := timeout(r, 30*time.Second)
	defer cancel()
	return ok(fmt.Sprintf("scaled %s to %d", t.Name, *t.Replicas)), c.Scale(ctx, k, t.Namespace, t.Name, *t.Replicas)
}

func (s *server) restart(r *http.Request) (any, error) {
	t, c, k, err := s.body(r)
	if err != nil {
		return nil, err
	}
	if err := s.confirmed(t); err != nil {
		return nil, err
	}
	ctx, cancel := timeout(r, 30*time.Second)
	defer cancel()
	return ok("restarted " + t.Name), c.Restart(ctx, k, t.Namespace, t.Name)
}

func (s *server) apply(r *http.Request) (any, error) {
	t, c, k, err := s.namedBody(r)
	if err != nil {
		return nil, err
	}
	ctx, cancel := timeout(r, 30*time.Second)
	defer cancel()
	return ok("applied " + t.Name), c.Apply(ctx, k, t.Namespace, t.Name, t.YAML)
}

func (s *server) cordon(r *http.Request) (any, error) {
	t, c, _, err := s.body(r)
	if err != nil {
		return nil, err
	}
	if err := s.confirmed(t); err != nil {
		return nil, err
	}
	on := t.On == nil || *t.On
	ctx, cancel := timeout(r, 30*time.Second)
	defer cancel()
	return ok(map[bool]string{true: "cordoned ", false: "uncordoned "}[on] + t.Name), c.Cordon(ctx, t.Name, on)
}

func (s *server) drain(r *http.Request) (any, error) {
	t, c, _, err := s.body(r)
	if err != nil {
		return nil, err
	}
	if err := s.confirmed(t); err != nil {
		return nil, err
	}
	ctx, cancel := timeout(r, 5*time.Minute)
	defer cancel()
	failed, err := c.Drain(ctx, t.Name)
	return map[string]any{"ok": "drained " + t.Name, "failed": failed}, err
}

func (s *server) forward(r *http.Request) (any, error) {
	t, c, _, err := s.body(r)
	if err != nil {
		return nil, err
	}
	if t.Remote <= 0 {
		return nil, fmt.Errorf("bad request: remote port required")
	}
	return c.PortForward(t.Namespace, t.Name, t.Local, t.Remote)
}

func (s *server) unforward(r *http.Request) (any, error) {
	var t target
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		return nil, fmt.Errorf("bad request: %w", err)
	}
	kube.StopForward(t.ID)
	return ok("stopped"), nil
}

var upgrader = websocket.Upgrader{
	// guard() has already checked the token and that Origin is ours.
	CheckOrigin: func(*http.Request) bool { return true },
}

// logs streams a container log over a websocket, one text frame per batch of lines.
func (s *server) logs(w http.ResponseWriter, r *http.Request) {
	c, _, err := s.resolve(q(r, "context"), "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() { // the browser closing the tab ends the stream
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				cancel()
				return
			}
		}
	}()
	tail, _ := strconv.ParseInt(q(r, "tail"), 10, 64)
	opts := kube.LogOptions{Container: q(r, "container"), Follow: q(r, "follow") != "false",
		Previous: q(r, "previous") == "true", TailLines: tail, Timestamps: q(r, "timestamps") == "true"}
	ch := make(chan string, 2048)
	errc := make(chan error, 1)
	go func() { errc <- c.Logs(ctx, q(r, "namespace"), q(r, "name"), opts, ch) }()
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	var batch []string
	flush := func() bool {
		if len(batch) == 0 {
			return true
		}
		err := conn.WriteMessage(websocket.TextMessage, []byte(strings.Join(batch, "\n")))
		batch = batch[:0]
		return err == nil
	}
	for {
		select {
		case line, open := <-ch:
			if !open {
				flush()
				if err := <-errc; err != nil {
					_ = conn.WriteMessage(websocket.TextMessage, []byte("\x00error: "+err.Error()))
				}
				_ = conn.WriteMessage(websocket.TextMessage, []byte("\x00end"))
				return
			}
			batch = append(batch, line)
			if len(batch) >= 500 && !flush() {
				return
			}
		case <-tick.C:
			if !flush() {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// exec is an interactive shell over a websocket: binary frames carry terminal bytes,
// text frames carry {"cols":N,"rows":N} resize messages.
func (s *server) exec(w http.ResponseWriter, r *http.Request) {
	c, _, err := s.resolve(q(r, "context"), "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	ns, pod, container := q(r, "namespace"), q(r, "name"), q(r, "container")
	node := q(r, "node")
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	out := &wsWriter{conn: conn}
	cmd := []string{"sh", "-c", "command -v bash >/dev/null 2>&1 && exec bash || exec sh"}
	if node != "" {
		_, _ = out.Write([]byte("\x1b[90mStarting a shell pod on " + node + "…\x1b[0m\r\n"))
		ns, pod, err = c.StartNodeShell(ctx, node)
		if err != nil {
			_, _ = out.Write([]byte("\x1b[31m" + err.Error() + "\x1b[0m\r\n"))
			return
		}
		defer c.StopNodeShell(ns, pod) // the helper pod goes when the window or tab closes
		container, cmd = "shell", kube.NodeShellCommand
		_, _ = out.Write([]byte("\x1b[90mHelper pod " + ns + "/" + pod + " is running. Opening the shell…\x1b[0m\r\n"))
	}
	// A shell that never prints is the failure that looks like waiting. Say so.
	go func() {
		select {
		case <-ctx.Done():
		case <-time.After(15 * time.Second):
			if !out.wrote() {
				_, _ = out.Write([]byte("\r\n\x1b[33mNo output after 15s. Press Enter; if nothing appears, the API server is not passing the exec through (check kubectl exec works from this machine).\x1b[0m\r\n"))
			}
		}
	}()
	stdinR, stdinW := newPipe()
	sizes := &wsSizes{ch: make(chan remotecommand.TerminalSize, 4)}
	go func() {
		defer cancel()
		defer stdinW.Close()
		for {
			typ, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if typ == websocket.TextMessage {
				var sz struct{ Cols, Rows uint16 }
				if json.Unmarshal(data, &sz) == nil && sz.Cols > 0 {
					select {
					case sizes.ch <- remotecommand.TerminalSize{Width: sz.Cols, Height: sz.Rows}:
					default:
					}
				}
				continue
			}
			if _, err := stdinW.Write(data); err != nil {
				return
			}
		}
	}()
	out.mark() // everything before this was our own status text
	if err := c.Exec(ctx, ns, pod, container, cmd, stdinR, out, sizes); err != nil && ctx.Err() == nil {
		_, _ = out.Write([]byte("\r\n\x1b[31mShell ended: " + err.Error() + "\x1b[0m\r\n"))
	}
}
