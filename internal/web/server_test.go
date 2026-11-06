package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/njahangirov/kube-console/internal/kube"
)

// A kubeconfig with one protected and one safe context. Nothing here reaches a
// cluster: the checks under test all refuse before a client is built.
const kubeconfig = `apiVersion: v1
kind: Config
current-context: shop-prod
clusters:
- name: c
  cluster: {server: "https://127.0.0.1:1"}
users:
- name: u
  user: {token: x}
contexts:
- name: shop-prod
  context: {cluster: c, user: u}
- name: payments-dev
  context: {cluster: c, user: u}
- name: kind-local
  context: {cluster: c, user: u}
`

func testServer(t *testing.T) (*server, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config")
	if err := os.WriteFile(p, []byte(kubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", p)
	t.Setenv("KV_AUDIT_LOG", filepath.Join(dir, "audit.log"))
	mgr, err := kube.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	s := &server{mgr: mgr, token: "tok", host: "127.0.0.1:7788"}
	return s, s.routes()
}

func do(h http.Handler, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = "127.0.0.1:7788"
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestTokenRequired(t *testing.T) {
	_, h := testServer(t)
	if w := do(h, "GET", "/api/contexts", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", w.Code)
	}
	if w := do(h, "GET", "/api/contexts", "", map[string]string{"X-KC-Token": "wrong"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", w.Code)
	}
	w := do(h, "GET", "/api/contexts", "", map[string]string{"X-KC-Token": "tok"})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "shop-prod") {
		t.Fatalf("good token: %d %s", w.Code, w.Body)
	}
}

func TestCrossOriginRefused(t *testing.T) {
	_, h := testServer(t)
	w := do(h, "POST", "/api/delete", `{}`, map[string]string{"X-KC-Token": "tok", "Origin": "https://evil.example"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin: %d", w.Code)
	}
}

func TestDNSRebindingRefused(t *testing.T) {
	_, h := testServer(t)
	w := do(h, "GET", "/api/contexts", "", map[string]string{"X-KC-Token": "tok", "Host": "evil.example:7788"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("foreign Host: %d", w.Code)
	}
}

func TestSingleWriteNeedsNoTypedName(t *testing.T) {
	_, h := testServer(t)
	hdr := map[string]string{"X-KC-Token": "tok", "Origin": "http://127.0.0.1:7788"}
	// A single change is confirmed with one click in the dialog: the server must not
	// ask for a typed name, even on a protected cluster. (No kind, so it fails later,
	// at the kind check -- before any API call.)
	w := do(h, "POST", "/api/restart", `{"context":"shop-prod","namespace":"a","name":"web"}`, hdr)
	if strings.Contains(w.Body.String(), "confirmation required") {
		t.Fatalf("single write asked for a typed name: %d %s", w.Code, w.Body)
	}
}

func TestDeleteAndApplyNeedTheName(t *testing.T) {
	_, h := testServer(t)
	hdr := map[string]string{"X-KC-Token": "tok", "Origin": "http://127.0.0.1:7788"}
	for _, path := range []string{"/api/delete", "/api/apply"} {
		for _, ctx := range []string{"shop-prod", "kind-local"} { // every cluster, protected or not
			w := do(h, "POST", path, `{"context":"`+ctx+`","kind":"pods","namespace":"a","name":"web","confirm":"wrong"}`, hdr)
			if w.Code != http.StatusPreconditionFailed || !strings.Contains(w.Body.String(), "type web and confirm") {
				t.Fatalf("%s on %s without the name: %d %s", path, ctx, w.Code, w.Body)
			}
		}
	}
}

func TestSafeContextNeedsNoTypedName(t *testing.T) {
	s, _ := testServer(t)
	if err := s.confirmed(target{Context: "kind-local", Name: "web"}); err != nil {
		t.Fatalf("safe context asked for confirmation: %v", err)
	}
	if err := s.confirmed(target{Context: "shop-prod", Name: "web", Confirm: "web"}); err != nil {
		t.Fatalf("correctly confirmed protected write refused: %v", err)
	}
}

func TestStaticUIServedWithoutToken(t *testing.T) {
	_, h := testServer(t)
	w := do(h, "GET", "/", "", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "K-Vision") {
		t.Fatalf("index: %d", w.Code)
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Fatalf("CSP missing: %q", csp)
	}
}

func TestRefusesNonLoopback(t *testing.T) {
	if err := Serve(nil, "0.0.0.0:0", false); err == nil || !strings.Contains(err.Error(), "only listens on localhost") {
		t.Fatalf("Serve on 0.0.0.0: %v", err)
	}
}

func TestSecretRevealIsPostOnly(t *testing.T) {
	_, h := testServer(t)
	hdr := map[string]string{"X-KC-Token": "tok", "Origin": "http://127.0.0.1:7788"}
	// GET must not reach the handler at all: values never travel in a URL.
	if w := do(h, "GET", "/api/secret?context=payments-dev&namespace=a&name=db", "", hdr); w.Code == http.StatusOK &&
		strings.Contains(w.Body.String(), "value") {
		t.Fatalf("GET revealed a secret: %d %s", w.Code, w.Body)
	}
}

func TestBulkDeleteGuards(t *testing.T) {
	_, h := testServer(t)
	hdr := map[string]string{"X-KC-Token": "tok", "Origin": "http://127.0.0.1:7788"}
	// One protected item in the batch means the phrase is required.
	body := `{"kind":"pods","items":[{"context":"payments-dev","namespace":"a","name":"x"},{"context":"shop-prod","namespace":"a","name":"y"}]}`
	w := do(h, "POST", "/api/delete-many", body, hdr)
	if w.Code != http.StatusPreconditionFailed || !strings.Contains(w.Body.String(), `type \"delete\"`) {
		t.Fatalf("unconfirmed bulk with a protected item: %d %s", w.Code, w.Body)
	}
	// Anything but the word "delete" is refused.
	w = do(h, "POST", "/api/delete-many", strings.Replace(body, `]}`, `],"confirm":"delete 2"}`, 1), hdr)
	if w.Code != http.StatusPreconditionFailed {
		t.Fatalf("wrong phrase accepted: %d", w.Code)
	}
	if w := do(h, "POST", "/api/delete-many", `{"kind":"pods","items":[]}`, hdr); w.Code != http.StatusBadRequest {
		t.Fatalf("empty selection: %d", w.Code)
	}
	var many []string
	for i := 0; i < MaxBulkDelete+1; i++ {
		many = append(many, `{"context":"payments-dev","namespace":"a","name":"p"}`)
	}
	if w := do(h, "POST", "/api/delete-many", `{"kind":"pods","items":[`+strings.Join(many, ",")+`]}`, hdr); w.Code != http.StatusBadRequest {
		t.Fatalf("over the cap: %d", w.Code)
	}
}

func TestDeleteContextNeedsTypedName(t *testing.T) {
	_, h := testServer(t)
	hdr := map[string]string{"X-KC-Token": "tok", "Origin": "http://127.0.0.1:7788"}
	w := do(h, "POST", "/api/context/delete", `{"context":"payments-dev"}`, hdr)
	if w.Code != http.StatusPreconditionFailed || !strings.Contains(w.Body.String(), "payments-dev") {
		t.Fatalf("unconfirmed delete: %d %s", w.Code, w.Body)
	}
	w = do(h, "POST", "/api/context/delete", `{"context":"shop-prod","confirm":"shop-prod"}`, hdr)
	if w.Code == http.StatusOK || !strings.Contains(w.Body.String(), "current context") {
		t.Fatalf("deleting the current context: %d %s", w.Code, w.Body)
	}
	w = do(h, "POST", "/api/context/delete", `{"context":"payments-dev","confirm":"payments-dev"}`, hdr)
	if w.Code != http.StatusOK {
		t.Fatalf("confirmed delete: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "Backup") {
		t.Fatalf("no backup reported: %s", w.Body)
	}
}

func TestCreateNeedsClusterNameOnProtected(t *testing.T) {
	_, h := testServer(t)
	hdr := map[string]string{"X-KC-Token": "tok", "Origin": "http://127.0.0.1:7788"}
	y := `{"context":"shop-prod","yaml":"apiVersion: v1\nkind: ConfigMap\nmetadata: {name: x}\n"}`
	w := do(h, "POST", "/api/create", y, hdr)
	if w.Code != http.StatusPreconditionFailed || !strings.Contains(w.Body.String(), "shop-prod") {
		t.Fatalf("unconfirmed create on protected: %d %s", w.Code, w.Body)
	}
	w = do(h, "POST", "/api/create", `{"context":"kind-local","yaml":""}`, hdr)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "no objects") {
		t.Fatalf("empty YAML: %d %s", w.Code, w.Body)
	}
}

func TestDownloadNames(t *testing.T) {
	for in, want := range map[string]string{"app.log": "app.log", "../../etc/passwd": "_.._etc_passwd", "a/b": "a_b", ".bashrc": "bashrc", "": "download"} {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
	dir := t.TempDir()
	first := uniquePath(dir, "app.log")
	_ = os.WriteFile(first, nil, 0o600)
	if second := uniquePath(dir, "app.log"); filepath.Base(second) != "app (2).log" {
		t.Fatalf("second download name: %s", second)
	}
}

func TestRevealOnlyInDownloads(t *testing.T) {
	_, h := testServer(t)
	hdr := map[string]string{"X-KC-Token": "tok", "Origin": "http://127.0.0.1:7788"}
	if w := do(h, "POST", "/api/files/reveal", `{"path":"/etc/passwd"}`, hdr); w.Code != http.StatusBadRequest {
		t.Fatalf("reveal outside Downloads: %d %s", w.Code, w.Body)
	}
}
