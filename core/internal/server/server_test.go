package server

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"math/big"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"
	"github.com/gofiber/fiber/v3"

	"github.com/Pukka922/edelweiss/core/internal/auth"
	"github.com/Pukka922/edelweiss/core/internal/config"
	"github.com/Pukka922/edelweiss/core/internal/extmgr"
	"github.com/Pukka922/edelweiss/core/internal/procman"
	"github.com/Pukka922/edelweiss/core/internal/session"
	"github.com/Pukka922/edelweiss/core/internal/testutil"
)

const testHash = "$6$Gx2pQr7sLm9tVw1z$nIdtw29mh8NZeWN5kYZQLBWb6sqwVSUcBmKfBiHmUkzZCjM2ZYEV6evPEf0UhG6fpFVTqemVFZDBMtL4bXs1.."
const testPassword = "edelweiss-test"

type fakeUsers map[string]*auth.UserInfo

func (f fakeUsers) Lookup(name string) (*auth.UserInfo, error) {
	if u, ok := f[name]; ok {
		return u, nil
	}
	return nil, errors.New("unknown user")
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type testEnv struct {
	app      *fiber.App
	sessions *session.Store
	pool     *procman.Manager
	logs     *syncBuffer // everything the server, the process manager and the extension store log
}

func newTestEnv(t *testing.T, mutate func(*Deps)) *testEnv {
	t.Helper()
	return newTestEnvWithData(t, "", mutate)
}

// newTestEnvWithData is newTestEnv with extDataRoot as the root of the data directories of extensions (a directory in
// a temporary location, never /var/lib; empty: the manager leaves data directories alone).
func newTestEnvWithData(t *testing.T, extDataRoot string, mutate func(*Deps)) *testEnv {
	t.Helper()
	state := testutil.TempDir(t)
	shadow := filepath.Join(state, "shadow")
	lines := "alice:" + testHash + ":20000::::::\nbob:" + testHash + ":20000::::::\nroot:" + testHash + ":20000::::::\n"
	if err := os.WriteFile(shadow, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	verifier, err := auth.NewVerifier(shadow)
	if err != nil {
		t.Fatal(err)
	}
	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	uid, _ := strconv.Atoi(me.Uid)
	gid, _ := strconv.Atoi(me.Gid)
	// All test accounts map to the current OS user so processes can be spawned without root.
	users := fakeUsers{
		"alice": {Name: "alice", UID: uid, GID: gid, Home: me.HomeDir, Groups: []string{"alice", "wheel"}},
		"bob":   {Name: "bob", UID: uid, GID: gid, Home: me.HomeDir, Groups: []string{"bob"}},
		"root":  {Name: "root", UID: uid, GID: gid, Home: me.HomeDir, Groups: []string{"root", "wheel"}},
	}
	authn := &auth.Authenticator{Verifier: verifier, Users: users, DenyUsers: []string{"root"}, AdminGroup: "wheel"}

	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	pool, err := procman.New(procman.Options{SocketDir: filepath.Join(t.TempDir(), "sockets"), IdleTimeout: time.Minute, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.StopAll)
	q, err := extmgr.NewExecQuerier()
	if err != nil {
		t.Fatal(err)
	}
	reg, err := extmgr.New(extmgr.Options{
		Dir: filepath.Join(state, "extensions"), TmpDir: filepath.Join(state, "tmp"), Querier: q, Logger: logger,
		ExtDataRoot: extDataRoot, OnChange: func(id string) { go pool.StopExtension(id) },
	})
	if err != nil {
		t.Fatal(err)
	}
	bin, err := testutil.BuildTestExt()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.InstallFile(context.Background(), bin, extmgr.Source{Type: "file", Location: bin}, ""); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.TLS.Enabled = false
	sessions := session.NewStore(15*time.Minute, 12*time.Hour)
	d := Deps{
		Config: cfg, Auth: authn, Sessions: sessions, Registry: reg, Pool: pool, Version: "test", Logger: logger,
		Hostname: func() string { return "testhost" },
		Shell: fstest.MapFS{
			"index.html":    {Data: []byte(`<!doctype html><html><head><script type="importmap">{"imports":{"solid-js":"/vendor/solid.js"}}</script></head><body>shell</body></html>`)},
			"assets/app.js": {Data: []byte("console.log(1)")},
		},
	}
	if mutate != nil {
		mutate(&d)
	}
	app, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	return &testEnv{app: app, sessions: sessions, pool: pool, logs: logs}
}

type sessionJSON struct {
	User struct {
		Name   string   `json:"name"`
		UID    int      `json:"uid"`
		Groups []string `json:"groups"`
	} `json:"user"`
	Admin     bool   `json:"admin"`
	CanAdmin  bool   `json:"canAdmin"`
	CSRFToken string `json:"csrfToken"`
	Hostname  string `json:"hostname"`
	Version   string `json:"version"`
}

type client struct {
	t      *testing.T
	app    *fiber.App
	cookie string
	csrf   string
}

func (c *client) do(method, path string, body any, headers ...string) *http.Response {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.cookie != "" {
		req.Header.Set("Cookie", "edelweiss_session="+c.cookie)
	}
	if c.csrf != "" && method != http.MethodGet {
		req.Header.Set("X-Csrf-Token", c.csrf)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := c.app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == "edelweiss_session" {
			c.cookie = ck.Value
		}
	}
	return resp
}

func (c *client) login(name, password string) (*http.Response, sessionJSON) {
	c.t.Helper()
	resp := c.do(http.MethodPost, "/api/auth/login", map[string]string{"username": name, "password": password})
	var s sessionJSON
	if resp.StatusCode == http.StatusOK {
		_ = json.NewDecoder(resp.Body).Decode(&s)
		c.csrf = s.CSRFToken
	}
	return resp, s
}

func errorCode(t *testing.T, resp *http.Response) string {
	t.Helper()
	var e struct{ Error struct{ Code string } }
	_ = json.NewDecoder(resp.Body).Decode(&e)
	return e.Error.Code
}

func TestHealth(t *testing.T) {
	env := newTestEnv(t, nil)
	resp := (&client{t: t, app: env.app}).do(http.MethodGet, "/api/health", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestLoginLogout(t *testing.T) {
	env := newTestEnv(t, nil)
	c := &client{t: t, app: env.app}

	if resp := c.do(http.MethodGet, "/api/session", nil); resp.StatusCode != 401 || errorCode(t, resp) != "unauthenticated" {
		t.Fatal("expected 401 without session")
	}
	if resp, _ := c.login("alice", "wrong"); resp.StatusCode != 401 || errorCode(t, resp) != "invalid_credentials" {
		t.Fatalf("wrong password: %d", resp.StatusCode)
	}
	if resp, _ := c.login("root", testPassword); resp.StatusCode != 401 {
		t.Fatalf("denied user root: %d", resp.StatusCode)
	}
	resp, s := c.login("alice", testPassword)
	if resp.StatusCode != 200 || s.User.Name != "alice" || !s.CanAdmin || s.Admin || s.CSRFToken == "" || s.Hostname != "testhost" {
		t.Fatalf("login: %d %+v", resp.StatusCode, s)
	}
	setCookie := resp.Header.Get("Set-Cookie")
	if !strings.Contains(setCookie, "HttpOnly") || !strings.Contains(strings.ToLower(setCookie), "samesite=strict") {
		t.Fatalf("cookie flags: %s", setCookie)
	}
	if resp := c.do(http.MethodGet, "/api/session", nil); resp.StatusCode != 200 {
		t.Fatalf("session: %d", resp.StatusCode)
	}

	csrf := c.csrf
	c.csrf = ""
	if resp := c.do(http.MethodPost, "/api/auth/logout", nil); resp.StatusCode != 403 || errorCode(t, resp) != "csrf_invalid" {
		t.Fatal("logout without CSRF token accepted")
	}
	c.csrf = csrf
	if resp := c.do(http.MethodPost, "/api/auth/logout", nil, "Origin", "http://evil.example"); resp.StatusCode != 403 {
		t.Fatal("logout with foreign origin accepted")
	}
	if resp := c.do(http.MethodPost, "/api/auth/logout", nil); resp.StatusCode != 204 {
		t.Fatalf("logout: %d", resp.StatusCode)
	}
	if resp := c.do(http.MethodGet, "/api/session", nil); resp.StatusCode != 401 {
		t.Fatal("session still valid after logout")
	}
}

func TestLoginRateLimit(t *testing.T) {
	env := newTestEnv(t, nil)
	c := &client{t: t, app: env.app}
	for i := 0; i < 10; i++ {
		c.login("bob", "wrong")
	}
	if resp, _ := c.login("bob", testPassword); resp.StatusCode != 429 {
		t.Fatalf("expected 429, got %d", resp.StatusCode)
	}
}

func TestLoginRateLimitCountsOnlyFailures(t *testing.T) {
	env := newTestEnv(t, nil)
	c := &client{t: t, app: env.app}
	for i := 0; i < 12; i++ {
		if resp, _ := c.login("bob", testPassword); resp.StatusCode != 200 {
			t.Fatalf("successful login %d: %d", i, resp.StatusCode)
		}
	}
	for i := 0; i < 10; i++ {
		c.login("bob", "wrong")
	}
	resp, _ := c.login("bob", "wrong")
	if resp.StatusCode != 429 || errorCode(t, resp) != "too_many_requests" || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("expected 429 too_many_requests with Retry-After, got %d", resp.StatusCode)
	}
}

func TestExtensionsAndProxy(t *testing.T) {
	env := newTestEnv(t, nil)
	c := &client{t: t, app: env.app}
	c.login("bob", testPassword)

	resp := c.do(http.MethodGet, "/api/extensions", nil)
	var list struct {
		Extensions []struct {
			ID   string `json:"id"`
			Icon string `json:"icon"`
			UI   struct {
				Entry string `json:"entry"`
			} `json:"ui"`
		} `json:"extensions"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&list)
	if len(list.Extensions) != 1 || list.Extensions[0].UI.Entry != "/ext/testext/ui/index.js" ||
		!strings.HasPrefix(list.Extensions[0].Icon, "data:image/svg+xml;base64,") {
		t.Fatalf("extensions: %+v", list)
	}

	resp = c.do(http.MethodGet, "/ext/testext/api/hello", nil, "Accept-Language", "de-DE,de;q=0.9")
	var hello struct {
		User  string `json:"user"`
		Admin bool   `json:"admin"`
		Lang  string `json:"lang"`
		OSUid int    `json:"osUid"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&hello)
	if resp.StatusCode != 200 || hello.User != "bob" || hello.Admin || hello.Lang != "de" || hello.OSUid != os.Getuid() {
		t.Fatalf("hello: %d %+v", resp.StatusCode, hello)
	}

	resp = c.do(http.MethodGet, "/ext/testext/api/version", nil)
	var version struct {
		Version string `json:"version"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&version)
	if resp.StatusCode != 200 || version.Version != "1.0.0" { // the version of testext's manifest.json
		t.Fatalf("version: %d %+v", resp.StatusCode, version)
	}

	resp = c.do(http.MethodGet, "/ext/testext/ui/index.js", nil)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "export default") {
		t.Fatalf("ui: %d %q", resp.StatusCode, body)
	}

	for _, p := range []string{
		"/ext/testext/api/_edelweiss/status", "/ext/testext/api/_EDELWEISS/status",
		"/ext/testext/api/%5Fedelweiss/status", "/ext/testext/api/%5fEDELWEISS/status",
		"/ext/testext/api/%2e%2e/_edelweiss/status", "/ext/testext/api/x/%2E%2E/%5Fedelweiss/status",
		"/ext/testext/api/x/../_edelweiss/status",
	} {
		if resp := c.do(http.MethodGet, p, nil); resp.StatusCode != 404 {
			t.Fatalf("%s reachable: %d", p, resp.StatusCode)
		}
	}
	csrf := c.csrf
	c.csrf = ""
	if resp := c.do(http.MethodPost, "/ext/testext/api/echo", map[string]int{"a": 1}); resp.StatusCode != 403 {
		t.Fatal("extension POST without CSRF accepted")
	}
	c.csrf = csrf
	resp = c.do(http.MethodPost, "/ext/testext/api/echo", map[string]int{"a": 1})
	body, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != `{"a":1}` {
		t.Fatalf("echo: %d %q", resp.StatusCode, body)
	}
	if resp := c.do(http.MethodGet, "/ext/nope/api/x", nil); resp.StatusCode != 404 || errorCode(t, resp) != "extension_not_found" {
		t.Fatal("unknown extension")
	}
	if resp := c.do(http.MethodDelete, "/api/extensions/testext", nil); resp.StatusCode != 403 || errorCode(t, resp) != "admin_required" {
		t.Fatal("non-admin removed an extension")
	}

	if len(env.pool.Running()) != 1 {
		t.Fatalf("running %v", env.pool.Running())
	}
	c.do(http.MethodPost, "/api/auth/logout", nil)
	deadline := time.Now().Add(10 * time.Second)
	for len(env.pool.Running()) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("processes not stopped after logout")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// countingPool records the specs of the extension processes the server asks for,
// i.e. the requests that reach an extension.
type countingPool struct {
	Pool
	mu    sync.Mutex
	specs []procman.Spec
}

func (p *countingPool) Acquire(ctx context.Context, spec procman.Spec) (*procman.Process, func(), error) {
	p.mu.Lock()
	p.specs = append(p.specs, spec)
	p.mu.Unlock()
	return p.Pool.Acquire(ctx, spec)
}

func (p *countingPool) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.specs)
}

func (p *countingPool) last() procman.Spec {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.specs[len(p.specs)-1]
}

// The reserved /_edelweiss namespace of an extension must be refused by the
// server itself, however the path is spelled; an extension that decodes the
// path before routing would otherwise serve it.
func TestReservedPathsNeverReachExtension(t *testing.T) {
	var pool *countingPool
	env := newTestEnv(t, func(d *Deps) { pool = &countingPool{Pool: d.Pool}; d.Pool = pool })
	c := &client{t: t, app: env.app}
	c.login("bob", testPassword)

	for _, p := range []string{
		"/ext/testext/api/_edelweiss", "/ext/testext/api/_edelweiss/", "/ext/testext/api/_edelweiss/status",
		"/ext/testext/api/_EDELWEISS/status", "/ext/testext/api/%5Fedelweiss/status", "/ext/testext/api/%5fEDELWEISS/status",
		"/ext/testext/api/_edelweiss%2fstatus", "/ext/testext/api/%2e%2e/_edelweiss/status",
		"/ext/testext/api/x/%2E%2E/%5Fedelweiss/status", "/ext/testext/api/x/../_edelweiss/status",
		"/ext/testext/api/x/..%2f_edelweiss/ui/index.js", "/ext/testext/api//_edelweiss/status",
		"/ext/testext/api/bad%zz/_edelweiss/status",
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.URL.Opaque = p // used verbatim as the request target
		req.Header.Set("Cookie", "edelweiss_session="+c.cookie)
		resp, err := env.app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if resp.StatusCode != 404 || pool.count() != 0 {
			t.Fatalf("%s: status %d, reached an extension process: %v", p, resp.StatusCode, pool.count() != 0)
		}
	}
	if resp := c.do(http.MethodGet, "/ext/testext/api/hello", nil); resp.StatusCode != 200 || pool.count() != 1 {
		t.Fatalf("regular path: %d, acquired %d", resp.StatusCode, pool.count())
	}
}

func TestAdminMode(t *testing.T) {
	env := newTestEnv(t, nil)
	bob := &client{t: t, app: env.app}
	bob.login("bob", testPassword)
	if resp := bob.do(http.MethodPost, "/api/session/admin", map[string]string{"password": testPassword}); resp.StatusCode != 403 || errorCode(t, resp) != "admin_not_permitted" {
		t.Fatal("bob got admin mode")
	}

	alice := &client{t: t, app: env.app}
	alice.login("alice", testPassword)
	if resp := alice.do(http.MethodPost, "/api/session/admin", map[string]string{"password": "wrong"}); resp.StatusCode != 401 {
		t.Fatalf("wrong admin password: %d", resp.StatusCode)
	}
	oldCookie := alice.cookie
	resp := alice.do(http.MethodPost, "/api/session/admin", map[string]string{"password": testPassword})
	var s sessionJSON
	_ = json.NewDecoder(resp.Body).Decode(&s)
	if resp.StatusCode != 200 || !s.Admin || alice.cookie == oldCookie {
		t.Fatalf("admin on: %d %+v", resp.StatusCode, s)
	}
	alice.csrf = s.CSRFToken
	stale := &client{t: t, app: env.app, cookie: oldCookie}
	if resp := stale.do(http.MethodGet, "/api/session", nil); resp.StatusCode != 401 {
		t.Fatal("old session id still valid after admin switch")
	}

	resp = alice.do(http.MethodGet, "/ext/testext/api/hello", nil)
	var hello struct {
		User  string `json:"user"`
		Admin bool   `json:"admin"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&hello)
	if hello.User != "alice" || !hello.Admin {
		t.Fatalf("admin hello %+v", hello)
	}

	resp = alice.do(http.MethodDelete, "/api/session/admin", nil)
	_ = json.NewDecoder(resp.Body).Decode(&s)
	if resp.StatusCode != 200 || s.Admin {
		t.Fatalf("admin off: %d %+v", resp.StatusCode, s)
	}
}

// Group membership can change while a session lives: admin mode is decided on
// the account as it is now, not as it was at login, and the session takes the
// fresh account over.
func TestAdminModeUsesCurrentGroups(t *testing.T) {
	var users fakeUsers
	env := newTestEnv(t, func(d *Deps) { users = d.Auth.(*auth.Authenticator).Users.(fakeUsers) })
	// replace (never modify) an account: the session holds on to the old value
	setGroups := func(name string, groups ...string) {
		u := *users[name]
		u.Groups = groups
		users[name] = &u
	}
	session := func(c *client) sessionJSON {
		t.Helper()
		var s sessionJSON
		resp := c.do(http.MethodGet, "/api/session", nil)
		if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&s) != nil {
			t.Fatalf("session: %d", resp.StatusCode)
		}
		return s
	}

	alice := &client{t: t, app: env.app}
	alice.login("alice", testPassword)
	setGroups("alice", "alice") // removed from wheel after the login
	if resp := alice.do(http.MethodPost, "/api/session/admin", map[string]string{"password": testPassword}); resp.StatusCode != 403 ||
		errorCode(t, resp) != "admin_not_permitted" {
		t.Fatalf("admin mode after leaving the admin group: %d", resp.StatusCode)
	}
	if s := session(alice); s.Admin || s.CanAdmin || strings.Join(s.User.Groups, ",") != "alice" {
		t.Fatalf("session after the refused switch: %+v", s)
	}

	bob := &client{t: t, app: env.app}
	bob.login("bob", testPassword)
	setGroups("bob", "bob", "wheel", "docker") // added after the login
	resp := bob.do(http.MethodPost, "/api/session/admin", map[string]string{"password": testPassword})
	var s sessionJSON
	_ = json.NewDecoder(resp.Body).Decode(&s)
	if resp.StatusCode != 200 || !s.Admin || !s.CanAdmin || strings.Join(s.User.Groups, ",") != "bob,wheel,docker" {
		t.Fatalf("admin mode after joining the admin group: %d %+v", resp.StatusCode, s)
	}
}

type adminOnlyRegistry struct{ Registry }

func (a adminOnlyRegistry) Get(id string) (*extmgr.Extension, bool) {
	e, ok := a.Registry.Get(id)
	if !ok {
		return nil, false
	}
	cp, m := *e, *e.Manifest
	m.RequiresAdmin = true
	cp.Manifest = &m
	return &cp, true
}

func TestRequiresAdminExtension(t *testing.T) {
	env := newTestEnv(t, func(d *Deps) { d.Registry = adminOnlyRegistry{d.Registry} })
	c := &client{t: t, app: env.app}
	c.login("alice", testPassword)
	if resp := c.do(http.MethodGet, "/ext/testext/api/hello", nil); resp.StatusCode != 403 || errorCode(t, resp) != "admin_required" {
		t.Fatalf("expected admin_required, got %d", resp.StatusCode)
	}
}

func TestShell(t *testing.T) {
	env := newTestEnv(t, nil)
	c := &client{t: t, app: env.app}

	resp := c.do(http.MethodGet, "/", nil)
	body, _ := io.ReadAll(resp.Body)
	csp := resp.Header.Get("Content-Security-Policy")
	if resp.StatusCode != 200 || !strings.Contains(string(body), "shell") || !strings.Contains(csp, "'sha256-") {
		t.Fatalf("index: %d csp=%q", resp.StatusCode, csp)
	}
	if resp := c.do(http.MethodGet, "/x/system/disks", nil); resp.StatusCode != 200 {
		t.Fatalf("spa fallback: %d", resp.StatusCode)
	}
	resp = c.do(http.MethodGet, "/assets/app.js", nil)
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("asset: %d %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	if resp := c.do(http.MethodGet, "/missing.js", nil); resp.StatusCode != 404 {
		t.Fatalf("missing asset: %d", resp.StatusCode)
	}
	if resp := c.do(http.MethodGet, "/api/nope", nil); resp.StatusCode != 404 || errorCode(t, resp) != "not_found" {
		t.Fatal("unknown api route")
	}
}

func TestPlaceholderShell(t *testing.T) {
	env := newTestEnv(t, func(d *Deps) { d.Shell = fstest.MapFS{} })
	resp := (&client{t: t, app: env.app}).do(http.MethodGet, "/", nil)
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "has not been built") {
		t.Fatalf("placeholder: %q", body)
	}
}

// Fiber does not percent-decode wildcard params, so the shell must do it itself.
func TestShellEncodedPaths(t *testing.T) {
	root := t.TempDir()
	shellDir := filepath.Join(root, "shell")
	if err := os.MkdirAll(filepath.Join(shellDir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		filepath.Join(shellDir, "index.html"):          `<!doctype html><html><body>shell</body></html>`,
		filepath.Join(shellDir, "assets", "my app.js"): "console.log(2)",
		filepath.Join(root, "secret.txt"):              "TOP-SECRET",
		filepath.Join(root, "secret"):                  "TOP-SECRET",
		filepath.Join(shellDir, "assets", "café.js"):   "console.log(3)",
	}
	for p, data := range files {
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env := newTestEnv(t, func(d *Deps) { d.Shell = os.DirFS(shellDir) })
	c := &client{t: t, app: env.app}

	resp := c.do(http.MethodGet, "/assets/my%20app.js", nil)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "console.log(2)" || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/javascript") {
		t.Fatalf("encoded asset name: %d %q %q", resp.StatusCode, body, resp.Header.Get("Content-Type"))
	}
	resp = c.do(http.MethodGet, "/assets/caf%C3%A9.js", nil)
	body, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "console.log(3)" {
		t.Fatalf("encoded utf-8 asset name: %d %q", resp.StatusCode, body)
	}

	for _, p := range []string{
		"/..%2fsecret.txt", "/..%2Fsecret.txt", "/%2e%2e/secret.txt", "/%2e%2e%2fsecret.txt",
		"/assets/..%2f..%2fsecret.txt", "/assets/%2e%2e%2f%2e%2e%2fsecret.txt", "/..%2fsecret", "/assets/..%2f..%2fsecret",
		"/..%5csecret.txt",
	} {
		resp := c.do(http.MethodGet, p, nil)
		body, _ := io.ReadAll(resp.Body)
		if strings.Contains(string(body), "TOP-SECRET") {
			t.Fatalf("%s served a file outside the shell root: %d %q", p, resp.StatusCode, body)
		}
	}
	// A name with an extension that is not part of the shell is a 404, never the SPA fallback.
	if resp := c.do(http.MethodGet, "/..%2fsecret.txt", nil); resp.StatusCode != 404 {
		t.Fatalf("encoded traversal: %d", resp.StatusCode)
	}
	// Invalid escapes are not found either (httptest.NewRequest cannot build
	// such a request line, so set the request URI by hand).
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.URL.Opaque = "/assets/%zz.js" // used verbatim as the request target
	resp, err := env.app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 404 {
		t.Fatalf("invalid escape: %d", resp.StatusCode)
	}
}

var errOperational = errors.New("read shadow file: open /etc/shadow: permission denied")

// loginFails fails Login operationally; everything else is delegated.
type loginFails struct{ Authenticator }

func (loginFails) Login(string, string) (*auth.UserInfo, error) { return nil, errOperational }

// reauthFails logs in normally but fails the admin re-authentication operationally.
type reauthFails struct{ Authenticator }

func (reauthFails) Reauthenticate(*auth.UserInfo, string) error { return errOperational }

// refreshFails logs in normally but cannot read the account again.
type refreshFails struct{ Authenticator }

func (refreshFails) Refresh(string) (*auth.UserInfo, error) { return nil, errOperational }

// Operational failures (unreadable shadow file, undecodable hash) must neither be
// mistaken for wrong credentials nor leak details to the client.
func TestOperationalAuthErrors(t *testing.T) {
	env := newTestEnv(t, func(d *Deps) { d.Auth = loginFails{d.Auth} })
	c := &client{t: t, app: env.app}
	resp, _ := c.login("alice", testPassword)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 500 || strings.Contains(string(body), "shadow") || strings.Contains(string(body), "permission") {
		t.Fatalf("login operational error: %d %q", resp.StatusCode, body)
	}
	var e struct{ Error struct{ Code string } }
	_ = json.Unmarshal(body, &e)
	if e.Error.Code != "internal_error" {
		t.Fatalf("login operational error code: %q", e.Error.Code)
	}

	env = newTestEnv(t, func(d *Deps) { d.Auth = reauthFails{d.Auth} })
	c = &client{t: t, app: env.app}
	if resp, _ := c.login("alice", testPassword); resp.StatusCode != 200 {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	resp = c.do(http.MethodPost, "/api/session/admin", map[string]string{"password": testPassword})
	body, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != 500 || strings.Contains(string(body), "shadow") || strings.Contains(string(body), "permission") {
		t.Fatalf("admin operational error: %d %q", resp.StatusCode, body)
	}

	env = newTestEnv(t, func(d *Deps) { d.Auth = refreshFails{d.Auth} })
	c = &client{t: t, app: env.app}
	if resp, _ := c.login("alice", testPassword); resp.StatusCode != 200 {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	resp = c.do(http.MethodPost, "/api/session/admin", map[string]string{"password": testPassword})
	body, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != 500 || strings.Contains(string(body), "shadow") || !strings.Contains(string(body), "internal_error") {
		t.Fatalf("admin account lookup error: %d %q", resp.StatusCode, body)
	}
	if !strings.Contains(env.logs.String(), "permission denied") {
		t.Fatalf("lookup error not logged:\n%s", env.logs.String())
	}
}

func TestLoginBodyHandling(t *testing.T) {
	env := newTestEnv(t, nil)
	c := &client{t: t, app: env.app}

	resp := c.do(http.MethodPost, "/api/auth/login", map[string]string{"username": "alice", "password": strings.Repeat("x", 70<<10)})
	if resp.StatusCode != 413 || errorCode(t, resp) != "payload_too_large" {
		t.Fatalf("oversized body: %d", resp.StatusCode)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader("{not json"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := env.app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 || errorCode(t, resp) != "invalid_json" {
		t.Fatalf("invalid json: %d", resp.StatusCode)
	}
}

func TestSessionCookieAttributes(t *testing.T) {
	env := newTestEnv(t, func(d *Deps) { d.Config.TLS.Enabled = true })
	c := &client{t: t, app: env.app}
	resp, _ := c.login("alice", testPassword)
	if got := strings.ToLower(resp.Header.Get("Set-Cookie")); !strings.Contains(got, "; secure") || !strings.Contains(got, "; httponly") ||
		!strings.Contains(got, "samesite=strict") || !strings.Contains(got, "path=/") ||
		strings.Contains(got, "max-age") || strings.Contains(got, "expires") {
		t.Fatalf("login cookie (tls): %s", got)
	}
	resp = c.do(http.MethodPost, "/api/auth/logout", nil)
	if got := strings.ToLower(resp.Header.Get("Set-Cookie")); resp.StatusCode != 204 ||
		!strings.Contains(got, "edelweiss_session=;") || (!strings.Contains(got, "max-age=0") && !strings.Contains(got, "expires=")) {
		t.Fatalf("logout cookie: %d %s", resp.StatusCode, got)
	}
}

// API answers carry sessions, CSRF tokens and system data: no cache (browser
// or intermediary) may keep them, whatever the outcome of the request.
func TestAPIResponsesAreNotStored(t *testing.T) {
	env := newTestEnv(t, nil)
	anon := &client{t: t, app: env.app}
	alice := &client{t: t, app: env.app}
	resp, _ := alice.login("alice", testPassword)
	check := func(what string, resp *http.Response) {
		t.Helper()
		if got := resp.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s (%d): Cache-Control %q, want no-store", what, resp.StatusCode, got)
		}
	}
	check("login", resp)
	check("failed login", anon.do(http.MethodPost, "/api/auth/login", map[string]string{"username": "alice", "password": "wrong"}))
	check("health", anon.do(http.MethodGet, "/api/health", nil))
	check("session without login", anon.do(http.MethodGet, "/api/session", nil))
	check("session", alice.do(http.MethodGet, "/api/session", nil))
	check("extensions", alice.do(http.MethodGet, "/api/extensions", nil))
	check("unknown api route", alice.do(http.MethodGet, "/api/nope", nil))
	check("oversized body", rawRequest(t, env.app, http.MethodPost, "/api/auth/login", bytes.Repeat([]byte("x"), 70<<10),
		"Content-Type", "application/json"))
	check("admin mode", alice.do(http.MethodPost, "/api/session/admin", map[string]string{"password": testPassword}))
	// the shell decides its own caching
	if got := anon.do(http.MethodGet, "/assets/app.js", nil).Header.Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("shell asset: Cache-Control %q", got)
	}
}

func TestSecurityHeaders(t *testing.T) {
	env := newTestEnv(t, nil)
	resp := (&client{t: t, app: env.app}).do(http.MethodGet, "/api/health", nil)
	if resp.Header.Get("X-Frame-Options") != "DENY" || resp.Header.Get("X-Content-Type-Options") != "nosniff" ||
		resp.Header.Get("Content-Security-Policy") == "" || resp.Header.Get("X-Request-Id") == "" {
		t.Fatalf("headers: %v", resp.Header)
	}
}

// serve runs app on a real listener: app.Test cannot show whether a response is
// streamed or buffered.
func serve(t *testing.T, app *fiber.App) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true}) }()
	// a bounded shutdown keeps a failing streaming test from hanging on its open stream
	t.Cleanup(func() { _ = app.ShutdownWithTimeout(2 * time.Second) })
	return ln.Addr().String()
}

// SSE and WebSocket traffic of an extension must pass the whole middleware stack
// of the server unbuffered.
func TestStreamingThroughServer(t *testing.T) {
	env := newTestEnv(t, nil)
	host := serve(t, env.app)
	// a buffering server would withhold the response headers: fail fast instead of hanging
	hc := &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 5 * time.Second}}

	body := `{"username":"bob","password":"` + testPassword + `"}`
	resp, err := hc.Post("http://"+host+"/api/auth/login", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var s sessionJSON
	_ = json.NewDecoder(resp.Body).Decode(&s)
	resp.Body.Close()
	var session *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == cookieSession {
			session = ck
		}
	}
	if resp.StatusCode != 200 || session == nil {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	withSession := func(r *http.Request) *http.Request {
		r.AddCookie(session)
		r.Header.Set(headerCSRF, s.CSRFToken)
		return r
	}

	req, _ := http.NewRequest(http.MethodGet, "http://"+host+"/ext/testext/api/events", nil)
	resp, err = hc.Do(withSession(req))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); resp.StatusCode != 200 || ct != "text/event-stream" {
		t.Fatalf("events: %d %q", resp.StatusCode, ct)
	}
	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	readUntil := func(want string) {
		t.Helper()
		timeout := time.After(5 * time.Second)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("stream closed before %q", want)
				}
				if l == want {
					return
				}
			case <-timeout:
				t.Fatalf("did not receive %q in time (response buffered?)", want)
			}
		}
	}
	readUntil("data: 1") // arrives while the extension is still blocked
	req, _ = http.NewRequest(http.MethodPost, "http://"+host+"/ext/testext/api/release", nil)
	rel, err := hc.Do(withSession(req))
	if err != nil {
		t.Fatal(err)
	}
	rel.Body.Close()
	if rel.StatusCode != 204 {
		t.Fatalf("release: %d", rel.StatusCode)
	}
	readUntil("data: 2")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws://" + host + "/ext/testext/api/ws"
	cookie := http.Header{"Cookie": {session.String()}}
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: cookie})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	if typ, data, err := conn.Read(ctx); err != nil || typ != websocket.MessageText || string(data) != "ping" {
		t.Fatalf("websocket echo: %v %q %v", typ, data, err)
	}
	conn.Close(websocket.StatusNormalClosure, "")

	// no session, no tunnel; a foreign origin gets none either
	if _, resp, err := websocket.Dial(ctx, wsURL, nil); err == nil || resp == nil || resp.StatusCode != 401 {
		t.Fatalf("websocket without session: resp=%v err=%v", resp, err)
	}
	foreign := http.Header{"Cookie": cookie["Cookie"], "Origin": {"http://evil.example"}}
	if _, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: foreign}); err == nil || resp == nil || resp.StatusCode != 403 {
		t.Fatalf("websocket from foreign origin: resp=%v err=%v", resp, err)
	}
}

// rawRequest sends a request whose target, headers and body are used verbatim
// (headers are added, so a name may repeat). A nil body sends none.
func rawRequest(t *testing.T, app *fiber.App, method, target string, body []byte, headers ...string) *http.Response {
	t.Helper()
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, "/", r)
	req.URL.Opaque = target // used verbatim as the request target
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Add(headers[i], headers[i+1])
	}
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	return resp
}

func gzipped(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func multipartBody(t *testing.T, size int) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("note", "hello"); err != nil {
		t.Fatal(err)
	}
	fw, err := mw.CreateFormFile("f", "big.bin")
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i * 7)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), mw.FormDataContentType()
}

// Fiber decompresses c.Body() up to app.BodyLimit, so a small encoded body would
// reach the handlers (and the login limiter key) hundreds of times larger than
// the API body limit. Encoded API bodies are refused before anything reads them.
func TestEncodedBodiesRejected(t *testing.T) {
	env := newTestEnv(t, nil)
	login := gzipped(t, []byte(`{"username":"alice","password":"`+testPassword+`"}`))
	for _, enc := range []string{"gzip", "x-gzip", "GZIP", "deflate", "br", "zstd", "compress", "gzip, identity", "identity, gzip", "bogus"} {
		resp := rawRequest(t, env.app, http.MethodPost, "/api/auth/login", login, "Content-Type", "application/json", "Content-Encoding", enc)
		if resp.StatusCode != 415 || errorCode(t, resp) != "unsupported_media_type" || !resp.Close {
			t.Fatalf("Content-Encoding %q: status %d, close %v", enc, resp.StatusCode, resp.Close)
		}
	}
	// the encoding may be spread over several header lines
	resp := rawRequest(t, env.app, http.MethodPost, "/api/auth/login", login, "Content-Type", "application/json",
		"Content-Encoding", "identity", "Content-Encoding", "gzip")
	if resp.StatusCode != 415 {
		t.Fatalf("split Content-Encoding: %d", resp.StatusCode)
	}
	// identity (or an empty value) is no encoding
	plain := []byte(`{"username":"alice","password":"` + testPassword + `"}`)
	for _, enc := range []string{"identity", "Identity", ""} {
		resp := rawRequest(t, env.app, http.MethodPost, "/api/auth/login", plain, "Content-Type", "application/json", "Content-Encoding", enc)
		if resp.StatusCode != 200 {
			t.Fatalf("Content-Encoding %q: %d", enc, resp.StatusCode)
		}
	}
	// refused requests never reach the limiters: no lockout from encoded attempts
	badUser := gzipped(t, []byte(`{"username":"bob","password":"wrong"}`))
	for i := 0; i < 15; i++ {
		resp := rawRequest(t, env.app, http.MethodPost, "/api/auth/login", badUser, "Content-Type", "application/json", "Content-Encoding", "gzip")
		if resp.StatusCode != 415 {
			t.Fatalf("encoded attempt %d: %d", i, resp.StatusCode)
		}
	}
	c := &client{t: t, app: env.app}
	if resp, _ := c.login("bob", testPassword); resp.StatusCode != 200 {
		t.Fatalf("login after encoded attempts: %d", resp.StatusCode)
	}
}

// A 60 KB gzip body inflates to 60 MiB, a brotli one of 2 KB to 1 GiB: nothing
// may inflate it.
func TestCompressionBombIsNeverInflated(t *testing.T) {
	env := newTestEnv(t, nil)
	bomb := gzipped(t, make([]byte, 60<<20))
	if len(bomb) >= apiBodyLimit {
		t.Fatalf("test bomb is %d bytes, not below the API body limit", len(bomb))
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	resp := rawRequest(t, env.app, http.MethodPost, "/api/auth/login", bomb, "Content-Type", "application/json", "Content-Encoding", "gzip")
	runtime.ReadMemStats(&after)
	if resp.StatusCode != 415 {
		t.Fatalf("bomb: %d", resp.StatusCode)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 16<<20 {
		t.Fatalf("handling a %d byte body allocated %d bytes: it was inflated", len(bomb), alloc)
	}
}

// fasthttp pre-reads and parses every multipart/form-data body with a known
// length before the first handler runs. The API must refuse an oversized one
// from its headers, while the client is still uploading.
func TestOversizedMultipartRejectedWhileUploading(t *testing.T) {
	env := newTestEnv(t, nil)
	host := serve(t, env.app)
	// ResponseHeaderTimeout only starts once the request is fully written, so bound the exchange itself
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pr, pw := io.Pipe()
	stalled := make(chan struct{})
	go func() {
		defer pw.Close()
		// the start of a well-formed form, then the client stalls: a parser waits for the rest
		head := "--edelweiss\r\nContent-Disposition: form-data; name=\"f\"; filename=\"a.bin\"\r\n\r\n"
		_, _ = pw.Write(append([]byte(head), bytes.Repeat([]byte("x"), 32<<10)...))
		select {
		case <-stalled:
		case <-ctx.Done():
		}
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+host+"/api/auth/login", pr)
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = 8 << 20
	req.Header.Set("Content-Type", "multipart/form-data; boundary=edelweiss")
	resp, err := http.DefaultClient.Do(req)
	close(stalled)
	if err != nil {
		t.Fatalf("no response while the client was still uploading (body read before the handlers ran?): %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 413 || errorCode(t, resp) != "payload_too_large" || !resp.Close {
		t.Fatalf("status %d, close %v", resp.StatusCode, resp.Close)
	}
}

// Uploads to extensions are streamed: the server must not let fasthttp pre-parse
// a multipart body (into memory and temp files) before any authentication ran,
// and the proxy must not have to reassemble it.
func TestMultipartBodiesAreStreamed(t *testing.T) {
	env := newTestEnv(t, nil)
	env.app.Post("/probe", func(c fiber.Ctx) error {
		streamed := c.Request().IsBodyStream()
		if streamed {
			_, _ = io.Copy(io.Discard, c.Request().BodyStream())
		}
		return c.JSON(fiber.Map{"stream": streamed})
	})
	body, ctype := multipartBody(t, 3<<20)
	resp := rawRequest(t, env.app, http.MethodPost, "/probe", body, "Content-Type", ctype)
	var probe struct{ Stream bool }
	_ = json.NewDecoder(resp.Body).Decode(&probe)
	if !probe.Stream {
		t.Fatal("the multipart body was pre-parsed before the first handler ran")
	}

	c := &client{t: t, app: env.app}
	c.login("bob", testPassword)
	resp = rawRequest(t, env.app, http.MethodPost, "/ext/testext/api/echo", body, "Content-Type", ctype,
		"Cookie", "edelweiss_session="+c.cookie, headerCSRF, c.csrf)
	echoed, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !bytes.Equal(echoed, body) {
		t.Fatalf("upload through the proxy: status %d, %d bytes echoed of %d", resp.StatusCode, len(echoed), len(body))
	}
}

// Admin mode asks for the same password as the login, so it needs the same
// protection against guessing: failures are limited per user.
func TestAdminModeRateLimit(t *testing.T) {
	env := newTestEnv(t, nil)
	alice := &client{t: t, app: env.app}
	alice.login("alice", testPassword)
	adminOn := func(c *client, password string) *http.Response {
		resp := c.do(http.MethodPost, "/api/session/admin", map[string]string{"password": password})
		if resp.StatusCode == 200 {
			var s sessionJSON
			_ = json.NewDecoder(resp.Body).Decode(&s)
			c.csrf = s.CSRFToken
		}
		return resp
	}
	for i := 0; i < 12; i++ { // successes are not counted
		if resp := adminOn(alice, testPassword); resp.StatusCode != 200 {
			t.Fatalf("admin on %d: %d", i, resp.StatusCode)
		}
	}
	for i := 0; i < 10; i++ {
		if resp := adminOn(alice, "wrong"); resp.StatusCode != 401 {
			t.Fatalf("wrong password %d: %d", i, resp.StatusCode)
		}
	}
	resp := adminOn(alice, testPassword)
	if resp.StatusCode != 429 || errorCode(t, resp) != "too_many_requests" || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("expected 429 too_many_requests with Retry-After, got %d", resp.StatusCode)
	}
	// the budget belongs to the user, not to the session
	other := &client{t: t, app: env.app}
	if resp, _ := other.login("alice", testPassword); resp.StatusCode != 200 {
		t.Fatalf("login of the rate-limited user: %d", resp.StatusCode)
	}
	if resp := adminOn(other, testPassword); resp.StatusCode != 429 {
		t.Fatalf("second session of the rate-limited user: %d", resp.StatusCode)
	}
}

// The login is the one unauthenticated state-changing route: a cross-site form
// must not be able to log a victim in as someone else.
func TestLoginRequiresJSONFromSameOrigin(t *testing.T) {
	env := newTestEnv(t, nil)
	body := []byte(`{"username":"alice","password":"` + testPassword + `"}`)
	post := func(headers ...string) *http.Response {
		return rawRequest(t, env.app, http.MethodPost, "/api/auth/login", body, headers...)
	}

	for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x", "application/jsonx", "json"} {
		var resp *http.Response
		if ct == "" {
			resp = post()
		} else {
			resp = post("Content-Type", ct)
		}
		if resp.StatusCode != 415 || errorCode(t, resp) != "unsupported_media_type" {
			t.Fatalf("Content-Type %q: %d", ct, resp.StatusCode)
		}
	}
	for _, ct := range []string{"application/json", "application/json; charset=utf-8", "Application/JSON"} {
		if resp := post("Content-Type", ct); resp.StatusCode != 200 {
			t.Fatalf("Content-Type %q: %d", ct, resp.StatusCode)
		}
	}

	// the test requests are sent to example.com
	if resp := post("Content-Type", "application/json", "Origin", "http://example.com"); resp.StatusCode != 200 {
		t.Fatalf("same origin: %d", resp.StatusCode)
	}
	for _, origin := range []string{"http://evil.example", "null", "https://example.com", "http://example.com:8080", "ftp://example.com", "http://example.com/x"} {
		resp := post("Content-Type", "application/json", "Origin", origin)
		if resp.StatusCode != 403 || errorCode(t, resp) != "origin_mismatch" {
			t.Fatalf("Origin %q: %d", origin, resp.StatusCode)
		}
	}

	// refused requests do not use up the failure budget of the account
	for i := 0; i < 15; i++ {
		if resp := post("Content-Type", "text/plain"); resp.StatusCode != 415 {
			t.Fatalf("attempt %d: %d", i, resp.StatusCode)
		}
		if resp := post("Content-Type", "application/json", "Origin", "http://evil.example"); resp.StatusCode != 403 {
			t.Fatalf("attempt %d: %d", i, resp.StatusCode)
		}
	}
	if resp := post("Content-Type", "application/json"); resp.StatusCode != 200 {
		t.Fatalf("login after refused attempts: %d", resp.StatusCode)
	}
}

// serveTLS runs app on a loopback listener with a throwaway certificate.
func serveTLS(t *testing.T, app *fiber.App) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "edelweiss-test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}})
	go func() { _ = app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true}) }()
	t.Cleanup(func() { _ = app.ShutdownWithTimeout(2 * time.Second) })
	return ln.Addr().String()
}

// Over TLS the Origin must be https (an http one is a downgrade, so cross-origin),
// and the default port of the origin is 443.
func TestSameOriginOverTLS(t *testing.T) {
	env := newTestEnv(t, func(d *Deps) { d.Config.TLS.Enabled = true })
	host := serveTLS(t, env.app)
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec // throwaway test certificate
	post := func(path string, session *http.Cookie, csrf, origin string, body string) *http.Response {
		req, err := http.NewRequest(http.MethodPost, "https://"+host+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		if session != nil {
			req.AddCookie(session)
			req.Header.Set(headerCSRF, csrf)
		}
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}
	login := `{"username":"bob","password":"` + testPassword + `"}`

	if resp := post("/api/auth/login", nil, "", "http://"+host, login); resp.StatusCode != 403 {
		t.Fatalf("login from an http origin: %d", resp.StatusCode)
	}
	resp := post("/api/auth/login", nil, "", "https://"+host, login)
	var s sessionJSON
	_ = json.NewDecoder(resp.Body).Decode(&s)
	var session *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == cookieSession {
			session = ck
		}
	}
	if resp.StatusCode != 200 || session == nil {
		t.Fatalf("login from the https origin: %d", resp.StatusCode)
	}
	if resp := post("/api/auth/logout", session, s.CSRFToken, "http://"+host, ""); resp.StatusCode != 403 {
		t.Fatalf("logout from an http origin: %d", resp.StatusCode)
	}
	if resp := post("/api/auth/logout", session, s.CSRFToken, "https://"+host, ""); resp.StatusCode != 204 {
		t.Fatalf("logout from the https origin: %d", resp.StatusCode)
	}
}

// The extension decodes the wildcard of a UI request itself, so the server has
// to look at what it will see: a path that leaves the UI directory, however it is
// spelled, never reaches the extension.
func TestUIPathsStayInsideTheUIDirectory(t *testing.T) {
	var pool *countingPool
	env := newTestEnv(t, func(d *Deps) { pool = &countingPool{Pool: d.Pool}; d.Pool = pool })
	c := &client{t: t, app: env.app}
	c.login("bob", testPassword)
	session := "edelweiss_session=" + c.cookie

	for _, p := range []string{
		"/ext/testext/ui/..%2f..%2f_edelweiss/status", "/ext/testext/ui/..%2F..%2F_edelweiss%2Fstatus",
		"/ext/testext/ui/%2e%2e/%2e%2e/_edelweiss/status", "/ext/testext/ui/%2E%2E%2F_edelweiss%2Fstatus",
		"/ext/testext/ui/a/..%2f..%2fb", "/ext/testext/ui/x/../../_edelweiss/status",
		"/ext/testext/ui/%2e%2e", "/ext/testext/ui/%2e", "/ext/testext/ui/%2findex.js", "/ext/testext/ui/a%00b.js",
		"/ext/testext/ui/bad%zz.js", "/ext/testext/ui/", "/ext/testext/ui/%2e%2e/index.js",
	} {
		resp := rawRequest(t, env.app, http.MethodGet, p, nil, "Cookie", session)
		if resp.StatusCode != 404 || pool.count() != 0 {
			t.Fatalf("%s: status %d, reached an extension process: %v", p, resp.StatusCode, pool.count() != 0)
		}
	}
	// plain files, also spelled with encoded or redundant parts, are forwarded
	for i, p := range []string{"/ext/testext/ui/index.js", "/ext/testext/ui/%69ndex.js", "/ext/testext/ui/x/../index.js", "/ext/testext/ui/./index.js"} {
		resp := rawRequest(t, env.app, http.MethodGet, p, nil, "Cookie", session)
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 || !strings.Contains(string(body), "export default") || pool.count() != i+1 {
			t.Fatalf("%s: status %d, acquired %d", p, resp.StatusCode, pool.count())
		}
	}
}

// Logging in again with a still valid cookie replaces that session instead of
// leaving it behind (and its processes running) until it idles out.
func TestReloginReplacesTheOldSession(t *testing.T) {
	env := newTestEnv(t, nil)
	waitStopped := func(what string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for len(env.pool.Running()) != 0 {
			if time.Now().After(deadline) {
				t.Fatalf("processes not stopped after %s: %v", what, env.pool.Running())
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	c := &client{t: t, app: env.app}
	c.login("alice", testPassword)
	first := c.cookie
	if resp, _ := c.login("alice", testPassword); resp.StatusCode != 200 || c.cookie == first {
		t.Fatalf("relogin: %d", resp.StatusCode)
	}
	stale := &client{t: t, app: env.app, cookie: first}
	if resp := stale.do(http.MethodGet, "/api/session", nil); resp.StatusCode != 401 {
		t.Fatalf("old session still valid after a new login: %d", resp.StatusCode)
	}
	if resp := c.do(http.MethodGet, "/api/session", nil); resp.StatusCode != 200 {
		t.Fatalf("new session: %d", resp.StatusCode)
	}

	// another user in the same browser: the first user is gone, so are the processes
	if resp := c.do(http.MethodGet, "/ext/testext/api/hello", nil); resp.StatusCode != 200 || len(env.pool.Running()) != 1 {
		t.Fatalf("alice hello: %d, running %v", resp.StatusCode, env.pool.Running())
	}
	if resp, s := c.login("bob", testPassword); resp.StatusCode != 200 || s.User.Name != "bob" {
		t.Fatalf("login as bob: %d", resp.StatusCode)
	}
	if env.sessions.HasSession("alice", false) {
		t.Fatal("alice still has a session")
	}
	waitStopped("switching to another user")

	// the same user without admin mode: the processes of the old admin session go
	alice := &client{t: t, app: env.app}
	alice.login("alice", testPassword)
	resp := alice.do(http.MethodPost, "/api/session/admin", map[string]string{"password": testPassword})
	var s sessionJSON
	_ = json.NewDecoder(resp.Body).Decode(&s)
	alice.csrf = s.CSRFToken
	if resp := alice.do(http.MethodGet, "/ext/testext/api/hello", nil); resp.StatusCode != 200 || len(env.pool.Running()) != 1 {
		t.Fatalf("admin hello: %d, running %v", resp.StatusCode, env.pool.Running())
	}
	if resp, s := alice.login("alice", testPassword); resp.StatusCode != 200 || s.Admin {
		t.Fatalf("relogin: %d %+v", resp.StatusCode, s)
	}
	waitStopped("leaving admin mode by logging in again")

	// a cookie that names no session does not matter
	other := &client{t: t, app: env.app, cookie: "no-such-session"}
	if resp, _ := other.login("bob", testPassword); resp.StatusCode != 200 {
		t.Fatalf("login with an unknown cookie: %d", resp.StatusCode)
	}
}

// The user name of a login attempt is chosen by the client: only a bounded part
// of it reaches the log (and the limiter key), cut at a character boundary.
func TestLoggedUsernameIsTruncated(t *testing.T) {
	env := newTestEnv(t, nil)
	c := &client{t: t, app: env.app}
	c.login(strings.Repeat("x", 300), "wrong")
	c.login(strings.Repeat("€", 100), "wrong") // 3 bytes per character
	logs := env.logs.String()
	if !strings.Contains(logs, "user="+strings.Repeat("x", 128)+" ") || strings.Contains(logs, strings.Repeat("x", 129)) {
		t.Fatalf("ascii name not cut at 128 bytes:\n%s", logs)
	}
	if !strings.Contains(logs, "user="+strings.Repeat("€", 42)+" ") || strings.Contains(logs, strings.Repeat("€", 43)) || strings.Contains(logs, `\x`) {
		t.Fatalf("multi-byte name not cut at a character boundary:\n%s", logs)
	}
}

// rootFails is an Authenticator that cannot resolve the admin-mode account.
type rootFails struct{ Authenticator }

func (rootFails) Root() (*auth.UserInfo, error) { return nil, errors.New("no such user: root") }

// enableAdmin logs c in as alice and switches admin mode on.
func enableAdmin(t *testing.T, c *client) {
	t.Helper()
	c.login("alice", testPassword)
	resp := c.do(http.MethodPost, "/api/session/admin", map[string]string{"password": testPassword})
	var s sessionJSON
	_ = json.NewDecoder(resp.Body).Decode(&s)
	if resp.StatusCode != 200 || !s.Admin {
		t.Fatalf("admin mode: %d %+v", resp.StatusCode, s)
	}
	c.csrf = s.CSRFToken
}

// Which process serves a request: the user's own, or in admin mode one of the
// root account that is kept apart from the user's non-admin one.
func TestProcessIdentity(t *testing.T) {
	var pool *countingPool
	env := newTestEnv(t, func(d *Deps) { pool = &countingPool{Pool: d.Pool}; d.Pool = pool })

	bob := &client{t: t, app: env.app}
	bob.login("bob", testPassword)
	if resp := bob.do(http.MethodGet, "/ext/testext/api/hello", nil); resp.StatusCode != 200 {
		t.Fatalf("bob: %d", resp.StatusCode)
	}
	spec := pool.last()
	if spec.Key != (procman.Key{User: "bob", Admin: false, ExtID: "testext"}) || spec.Identity.Name != "bob" || spec.Binary == "" {
		t.Fatalf("bob's process: %+v", spec)
	}

	alice := &client{t: t, app: env.app}
	alice.login("alice", testPassword)
	if resp := alice.do(http.MethodGet, "/ext/testext/api/hello", nil); resp.StatusCode != 200 {
		t.Fatalf("alice: %d", resp.StatusCode)
	}
	if spec := pool.last(); spec.Key != (procman.Key{User: "alice", Admin: false, ExtID: "testext"}) || spec.Identity.Name != "alice" {
		t.Fatalf("alice's process: %+v", spec)
	}
	enableAdmin(t, alice)
	if resp := alice.do(http.MethodGet, "/ext/testext/api/hello", nil); resp.StatusCode != 200 {
		t.Fatalf("alice in admin mode: %d", resp.StatusCode)
	}
	if spec := pool.last(); spec.Key != (procman.Key{User: "alice", Admin: true, ExtID: "testext"}) || spec.Identity.Name != "root" {
		t.Fatalf("alice's admin process: %+v", spec)
	}
	if len(env.pool.Running()) != 3 {
		t.Fatalf("running %v", env.pool.Running())
	}
}

func TestAdminProcessNeedsTheRootAccount(t *testing.T) {
	env := newTestEnv(t, func(d *Deps) { d.Auth = rootFails{d.Auth} })
	alice := &client{t: t, app: env.app}
	enableAdmin(t, alice)
	resp := alice.do(http.MethodGet, "/ext/testext/api/hello", nil)
	body, _ := io.ReadAll(resp.Body)
	var e struct{ Error struct{ Code string } }
	_ = json.Unmarshal(body, &e)
	if resp.StatusCode != 503 || e.Error.Code != "backend_unavailable" || strings.Contains(string(body), "root") {
		t.Fatalf("status %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(env.logs.String(), "resolve process identity") {
		t.Fatalf("not logged:\n%s", env.logs.String())
	}
	if len(env.pool.Running()) != 0 {
		t.Fatalf("a process was started: %v", env.pool.Running())
	}
}

func TestRemoveExtension(t *testing.T) {
	env := newTestEnv(t, nil)
	alice := &client{t: t, app: env.app}
	enableAdmin(t, alice)

	if resp := alice.do(http.MethodDelete, "/api/extensions/nope", nil); resp.StatusCode != 404 || errorCode(t, resp) != "extension_not_found" {
		t.Fatalf("unknown extension: %d", resp.StatusCode)
	}
	if resp := alice.do(http.MethodGet, "/ext/testext/api/hello", nil); resp.StatusCode != 200 { // a process to stop
		t.Fatalf("hello: %d", resp.StatusCode)
	}
	if resp := alice.do(http.MethodDelete, "/api/extensions/testext", nil); resp.StatusCode != 204 {
		t.Fatalf("remove: %d", resp.StatusCode)
	}
	var list struct{ Extensions []json.RawMessage }
	_ = json.NewDecoder(alice.do(http.MethodGet, "/api/extensions", nil).Body).Decode(&list)
	if len(list.Extensions) != 0 {
		t.Fatalf("still listed: %s", list.Extensions)
	}
	if resp := alice.do(http.MethodDelete, "/api/extensions/testext", nil); resp.StatusCode != 404 || errorCode(t, resp) != "extension_not_found" {
		t.Fatalf("second remove: %d", resp.StatusCode)
	}
	if resp := alice.do(http.MethodGet, "/ext/testext/api/hello", nil); resp.StatusCode != 404 || errorCode(t, resp) != "extension_not_found" {
		t.Fatalf("removed extension still served: %d", resp.StatusCode)
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(env.pool.Running()) != 0 { // the store's OnChange stops its processes
		if time.Now().After(deadline) {
			t.Fatalf("processes of the removed extension still running: %v", env.pool.Running())
		}
		time.Sleep(50 * time.Millisecond)
	}
	logs := env.logs.String()
	// one audit line with the acting user, one from the store
	if !strings.Contains(logs, "msg=\"extension removed\" id=testext user=alice") || strings.Count(logs, "msg=\"extension removed\"") != 1 ||
		strings.Count(logs, "msg=\"extension store: removed\" id=testext") != 1 {
		t.Fatalf("removal not logged once:\n%s", logs)
	}
}

func TestRemoveExtensionDeletesItsDataDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "varlib")
	data, other := filepath.Join(root, "edelweiss-ext-testext"), filepath.Join(root, "edelweiss-ext-other")
	for _, d := range []string{data, other} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "registries.db"), []byte("x"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	env := newTestEnvWithData(t, root, nil)
	alice := &client{t: t, app: env.app}
	enableAdmin(t, alice)

	if resp := alice.do(http.MethodDelete, "/api/extensions/testext", nil); resp.StatusCode != 204 {
		t.Fatalf("remove: %d", resp.StatusCode)
	}
	if _, err := os.Stat(data); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the data directory is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(other, "registries.db")); err != nil {
		t.Fatalf("the data of another extension was touched: %v", err)
	}
}

// A data directory that cannot be deleted must not go unnoticed: the extension is gone, so there is nothing to retry.
func TestRemoveExtensionReportsADataDirectoryItCannotDelete(t *testing.T) {
	// A root that is a regular file: nothing below it can be deleted (ENOTDIR), whoever runs the test.
	root := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(root, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "edelweiss-ext-testext")
	env := newTestEnvWithData(t, root, nil)
	alice := &client{t: t, app: env.app}
	enableAdmin(t, alice)

	resp := alice.do(http.MethodDelete, "/api/extensions/testext", nil)
	var body struct {
		Error struct{ Code, Message string }
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != 500 || body.Error.Code != "data_not_removed" ||
		body.Error.Message != "the extension was removed, but its data directory "+dir+" could not be deleted" {
		t.Fatalf("remove: %d %+v", resp.StatusCode, body)
	}
	var list struct{ Extensions []json.RawMessage }
	_ = json.NewDecoder(alice.do(http.MethodGet, "/api/extensions", nil).Body).Decode(&list)
	if len(list.Extensions) != 0 {
		t.Fatalf("still listed: %s", list.Extensions)
	}
	if resp := alice.do(http.MethodDelete, "/api/extensions/testext", nil); resp.StatusCode != 404 {
		t.Fatalf("second remove: %d", resp.StatusCode)
	}
	// the removal is audited as usual, and the cause is logged for the operator
	logs := env.logs.String()
	if !strings.Contains(logs, "msg=\"extension removed\" id=testext user=alice") || strings.Count(logs, "msg=\"extension removed\"") != 1 ||
		!strings.Contains(logs, "data directory not deleted") || !strings.Contains(logs, "not a directory") {
		t.Fatalf("not logged as expected:\n%s", logs)
	}
}

// Every unsafe route needs the token of the session, not just the session cookie.
func TestUnsafeRoutesNeedTheCSRFToken(t *testing.T) {
	env := newTestEnv(t, nil)
	alice := &client{t: t, app: env.app}
	enableAdmin(t, alice)
	token := alice.csrf

	for _, csrf := range []string{"", "bogus", token + "x", token[:len(token)-1]} {
		alice.csrf = csrf
		for _, r := range []struct {
			method, path string
			body         any
		}{
			{http.MethodPost, "/api/session/admin", map[string]string{"password": testPassword}},
			{http.MethodDelete, "/api/session/admin", nil},
			{http.MethodDelete, "/api/extensions/testext", nil},
			{http.MethodPost, "/api/extensions/resolve", map[string]string{"source": "a/b"}},
			{http.MethodPost, "/api/extensions/install", map[string]string{"source": "a/b"}},
			{http.MethodPost, "/api/extensions/testext/update", nil},
			{http.MethodPost, "/api/auth/logout", nil},
			{http.MethodPost, "/ext/testext/api/echo", map[string]int{"a": 1}},
		} {
			var resp *http.Response
			if csrf == "" {
				resp = alice.do(r.method, r.path, r.body)
			} else {
				resp = alice.do(r.method, r.path, r.body, headerCSRF, csrf)
			}
			if resp.StatusCode != 403 || errorCode(t, resp) != "csrf_invalid" {
				t.Fatalf("%s %s with token %q: %d", r.method, r.path, csrf, resp.StatusCode)
			}
		}
	}
	// nothing happened
	alice.csrf = token
	var s sessionJSON
	resp := alice.do(http.MethodGet, "/api/session", nil)
	_ = json.NewDecoder(resp.Body).Decode(&s)
	if resp.StatusCode != 200 || !s.Admin {
		t.Fatalf("session: %d %+v", resp.StatusCode, s)
	}
	var list struct{ Extensions []json.RawMessage }
	_ = json.NewDecoder(alice.do(http.MethodGet, "/api/extensions", nil).Body).Decode(&list)
	if len(list.Extensions) != 1 {
		t.Fatalf("extensions: %s", list.Extensions)
	}
}

// requiresAdmin applies to the UI files of an extension as well as to its API.
func TestRequiresAdminExtensionUI(t *testing.T) {
	var pool *countingPool
	env := newTestEnv(t, func(d *Deps) {
		pool = &countingPool{Pool: d.Pool}
		d.Pool = pool
		d.Registry = adminOnlyRegistry{d.Registry}
	})
	c := &client{t: t, app: env.app}
	c.login("alice", testPassword)
	if resp := c.do(http.MethodGet, "/ext/testext/ui/index.js", nil); resp.StatusCode != 403 || errorCode(t, resp) != "admin_required" || pool.count() != 0 {
		t.Fatalf("ui without admin mode: %d, acquired %d", resp.StatusCode, pool.count())
	}
	enableAdmin(t, c)
	resp := c.do(http.MethodGet, "/ext/testext/ui/index.js", nil)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "export default") {
		t.Fatalf("ui in admin mode: %d", resp.StatusCode)
	}
}

// The audit trail: who logged in, failed to, switched admin mode, logged out. It
// names accounts and addresses, never secrets.
func TestAuditLog(t *testing.T) {
	env := newTestEnv(t, nil)
	c := &client{t: t, app: env.app}
	secrets := []string{"wrong-secret", "another-wrong-secret", testPassword}
	// step sends a request and remembers the session id and CSRF token it ends up with
	step := func(method, path string, body any) {
		t.Helper()
		resp := c.do(method, path, body)
		var s sessionJSON
		if resp.StatusCode == 200 && json.NewDecoder(resp.Body).Decode(&s) == nil && s.CSRFToken != "" {
			c.csrf = s.CSRFToken
			secrets = append(secrets, s.CSRFToken)
		}
		secrets = append(secrets, c.cookie)
	}
	c.login("bob", "wrong-secret")
	step(http.MethodPost, "/api/auth/login", map[string]string{"username": "alice", "password": testPassword})
	step(http.MethodPost, "/api/session/admin", map[string]string{"password": "another-wrong-secret"})
	step(http.MethodPost, "/api/session/admin", map[string]string{"password": testPassword})
	step(http.MethodDelete, "/api/session/admin", nil)
	step(http.MethodPost, "/api/auth/logout", nil)

	// (the store's install line names a temp path that contains the test password)
	var audit []string
	for _, line := range strings.Split(env.logs.String(), "\n") {
		if !strings.Contains(line, `msg="extension store: installed"`) {
			audit = append(audit, line)
		}
	}
	logs := strings.Join(audit, "\n")
	for _, want := range []string{
		`msg="login failed" user=bob ip=`, `msg=login user=alice ip=`, `msg="admin mode denied" user=alice ip=`,
		`msg="admin mode enabled" user=alice ip=`, `msg="admin mode disabled" user=alice`, `msg=logout user=alice`,
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("no %q in the log:\n%s", want, logs)
		}
	}
	for _, secret := range secrets {
		if secret != "" && strings.Contains(logs, secret) {
			t.Errorf("secret %q in the log:\n%s", secret, logs)
		}
	}
}
