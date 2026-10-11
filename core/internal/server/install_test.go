package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/Pukka922/edelweiss/core/internal/extmgr"
	"github.com/Pukka922/edelweiss/core/internal/github"
	"github.com/Pukka922/edelweiss/sdk/manifest"
)

type fakeInstaller struct {
	mu       sync.Mutex
	resolve  func(source string) (*extmgr.Resolution, error)
	install  func(req extmgr.InstallRequest) (*extmgr.Extension, error)
	updates  func() ([]extmgr.Update, []extmgr.UpdateError)
	update   func(id, sha string) (*extmgr.Extension, error)
	requests []string
}

func (f *fakeInstaller) record(s string) {
	f.mu.Lock()
	f.requests = append(f.requests, s)
	f.mu.Unlock()
}

func (f *fakeInstaller) Resolve(_ context.Context, source string) (*extmgr.Resolution, error) {
	f.record("resolve " + source)
	return f.resolve(source)
}
func (f *fakeInstaller) Install(_ context.Context, req extmgr.InstallRequest) (*extmgr.Extension, error) {
	f.record(fmt.Sprintf("install %s %s %s", req.Source, req.ID, req.SHA256))
	return f.install(req)
}
func (f *fakeInstaller) Updates(context.Context) ([]extmgr.Update, []extmgr.UpdateError) {
	f.record("updates")
	return f.updates()
}
func (f *fakeInstaller) Update(_ context.Context, id, confirmed string) (*extmgr.Extension, error) {
	f.record(fmt.Sprintf("update %s %s", id, confirmed))
	return f.update(id, confirmed)
}

var helloSum = strings.Repeat("ab", 32)

func helloExt(version string) *extmgr.Extension {
	return &extmgr.Extension{
		Manifest: &manifest.Manifest{ID: "hello", Version: version, APIVersion: 1, Name: manifest.Localized{"en": "Hello"},
			Menu: manifest.Menu{Section: "tools", Order: 90}},
		Source: extmgr.Source{Type: "github", Location: "github.com/edelweiss/hello", Repo: "edelweiss/hello",
			Tag: "v" + version, Asset: "edelweiss-ext-hello_" + version + "_linux_amd64", SHA256: helloSum,
			InstalledAt: time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)},
	}
}

func newInstallEnv(t *testing.T, f *fakeInstaller) (*testEnv, *client) {
	t.Helper()
	env := newTestEnv(t, func(d *Deps) { d.Installer = f })
	alice := &client{t: t, app: env.app}
	enableAdmin(t, alice)
	return env, alice
}

func TestInstallRoutesNeedAdminMode(t *testing.T) {
	f := &fakeInstaller{}
	env := newTestEnv(t, func(d *Deps) { d.Installer = f })
	alice := &client{t: t, app: env.app}
	if resp, _ := alice.login("alice", testPassword); resp.StatusCode != 200 {
		t.Fatal("login")
	}
	for _, r := range []struct{ method, path string }{
		{http.MethodPost, "/api/extensions/resolve"},
		{http.MethodPost, "/api/extensions/install"},
		{http.MethodGet, "/api/extensions/updates"},
		{http.MethodPost, "/api/extensions/hello/update"},
	} {
		if resp := alice.do(r.method, r.path, map[string]string{"source": "edelweiss/hello"}); resp.StatusCode != 403 || errorCode(t, resp) != "admin_required" {
			t.Errorf("%s %s without admin mode: %d", r.method, r.path, resp.StatusCode)
		}
	}
	if len(f.requests) != 0 {
		t.Fatalf("installer called: %v", f.requests)
	}
}

func TestResolveRoute(t *testing.T) {
	res := &extmgr.Resolution{Repo: "edelweiss/hello", Tag: "v0.1.0", Name: "v0.1.0", URL: "https://example/rel",
		PublishedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		Candidates:  []extmgr.Candidate{{ID: "hello", Version: "0.1.0", Arch: "amd64", Asset: "edelweiss-ext-hello_0.1.0_linux_amd64", Size: 42, SHA256: helloSum}}}
	f := &fakeInstaller{resolve: func(string) (*extmgr.Resolution, error) { return res, nil }}
	_, alice := newInstallEnv(t, f)
	resp := alice.do(http.MethodPost, "/api/extensions/resolve", map[string]string{"source": "edelweiss/hello@v0.1.0"})
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var got map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&got)
	cands, _ := got["candidates"].([]any)
	first, _ := cands[0].(map[string]any)
	if got["repo"] != "edelweiss/hello" || got["tag"] != "v0.1.0" || got["publishedAt"] != "2026-10-01T00:00:00Z" ||
		len(cands) != 1 || first["sha256"] != helloSum || first["asset"] != "edelweiss-ext-hello_0.1.0_linux_amd64" || first["size"] != float64(42) {
		t.Fatalf("answer %v", got)
	}
	if f.requests[0] != "resolve edelweiss/hello@v0.1.0" {
		t.Fatalf("requests %v", f.requests)
	}
}

func TestInstallerErrorMapping(t *testing.T) {
	f := &fakeInstaller{}
	_, alice := newInstallEnv(t, f) // one environment; each case only swaps the installer's answer
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{fmt.Errorf("%w: x", github.ErrInvalidSource), 400, "invalid_source"},
		{fmt.Errorf("%w: x", github.ErrReleaseNotFound), 404, "release_not_found"},
		{fmt.Errorf("%w: x", github.ErrRateLimited), 503, "github_rate_limited"},
		{fmt.Errorf("%w: x", github.ErrUnavailable), 502, "github_unavailable"},
		{fmt.Errorf("%w: x", github.ErrTooLarge), 413, "asset_too_large"},
		{fmt.Errorf("%w: x", extmgr.ErrNoCandidates), 422, "no_matching_assets"},
		{fmt.Errorf("%w: x", extmgr.ErrNoChecksums), 422, "checksums_missing"},
		{fmt.Errorf("%w: x", extmgr.ErrChecksumMismatch), 422, "checksum_mismatch"},
		{fmt.Errorf("%w: x", extmgr.ErrReleaseChanged), 409, "release_changed"},
		{fmt.Errorf("%w: x", extmgr.ErrChooseID), 400, "id_required"},
		{fmt.Errorf("%w: x", extmgr.ErrUnknownCandidate), 400, "unknown_id"},
		{fmt.Errorf("%w: x", extmgr.ErrBadArchive), 422, "invalid_extension"},
		{fmt.Errorf("%w: %w", extmgr.ErrInvalidExtension, extmgr.ErrNotStatic), 422, "invalid_extension"},
		{fmt.Errorf("%w: x", extmgr.ErrNotFromGitHub), 409, "not_from_github"},
		{fmt.Errorf("%w: x", extmgr.ErrUpToDate), 409, "up_to_date"},
		{extmgr.ErrBusy, 409, "operation_in_progress"},
		{context.DeadlineExceeded, 504, "timeout"},
		{context.Canceled, 504, "timeout"},
		// a timeout of the request to GitHub is a timeout, not "GitHub is unavailable"
		{fmt.Errorf("%w: %w", github.ErrUnavailable, context.DeadlineExceeded), 504, "timeout"},
		{fmt.Errorf("%w: %w", github.ErrUnavailable, context.Canceled), 504, "timeout"},
		{errors.New("disk on fire"), 500, "internal_error"},
	} {
		f.install = func(extmgr.InstallRequest) (*extmgr.Extension, error) { return nil, tc.err }
		resp := alice.do(http.MethodPost, "/api/extensions/install", map[string]string{"source": "a/b"})
		var body struct {
			Error struct{ Code, Message string }
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		if resp.StatusCode != tc.status || body.Error.Code != tc.code {
			t.Errorf("%v: got %d %s, want %d %s", tc.err, resp.StatusCode, body.Error.Code, tc.status, tc.code)
		}
		if tc.status == 500 && (body.Error.Message != "the install failed" || strings.Contains(body.Error.Message, "disk on fire")) {
			t.Errorf("%v: message %q must be the generic one", tc.err, body.Error.Message)
		}
	}
}

// blockingInstaller's Install waits for the end of its context, like a download that is still running.
type blockingInstaller struct {
	*fakeInstaller
	started chan struct{}
}

func (b *blockingInstaller) Install(ctx context.Context, _ extmgr.InstallRequest) (*extmgr.Extension, error) {
	close(b.started)
	<-ctx.Done()
	return nil, fmt.Errorf("%w: download: %w", github.ErrUnavailable, ctx.Err()) // as the GitHub client reports it
}

// A running install is cut off when the server shuts down. This needs a real listener: only a served
// fasthttp server has a Done channel for its request contexts.
func TestInstallEndsWhenTheServerShutsDown(t *testing.T) {
	inst := &blockingInstaller{fakeInstaller: &fakeInstaller{}, started: make(chan struct{})}
	env := newTestEnv(t, func(d *Deps) { d.Installer = inst })
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- env.app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true}) }()

	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar, Timeout: 20 * time.Second}
	post := func(path, csrf string, body any) (*http.Response, error) {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, "http://"+ln.Addr().String()+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		if csrf != "" {
			req.Header.Set(headerCSRF, csrf)
		}
		return hc.Do(req)
	}
	resp, err := post("/api/auth/login", "", map[string]string{"username": "alice", "password": testPassword})
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("login: %v %v", resp, err)
	}
	var sess sessionJSON
	_ = json.NewDecoder(resp.Body).Decode(&sess)
	resp.Body.Close()
	if resp, err = post("/api/session/admin", sess.CSRFToken, map[string]string{"password": testPassword}); err != nil || resp.StatusCode != 200 {
		t.Fatalf("admin mode: %v %v", resp, err)
	}
	_ = json.NewDecoder(resp.Body).Decode(&sess) // admin mode comes with a new CSRF token
	resp.Body.Close()

	type answer struct {
		status int
		code   string
		err    error
	}
	answered := make(chan answer, 1)
	go func() {
		resp, err := post("/api/extensions/install", sess.CSRFToken, map[string]string{"source": "a/b"})
		if err != nil {
			answered <- answer{err: err}
			return
		}
		defer resp.Body.Close()
		answered <- answer{status: resp.StatusCode, code: errorCode(t, resp)}
	}()
	select {
	case <-inst.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the installer was not called")
	}
	shutdown := make(chan error, 1)
	go func() { shutdown <- env.app.ShutdownWithTimeout(5 * time.Second) }()
	select {
	case a := <-answered:
		if a.err != nil || a.status != 504 || a.code != "timeout" {
			t.Fatalf("answer %+v", a)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the install kept running after the server began to shut down")
	}
	if err := <-shutdown; err != nil {
		t.Errorf("shutdown: %v", err)
	}
	<-served
}

func TestInstallRoute(t *testing.T) {
	f := &fakeInstaller{install: func(extmgr.InstallRequest) (*extmgr.Extension, error) { return helloExt("0.1.0"), nil }}
	env, alice := newInstallEnv(t, f)
	resp := alice.do(http.MethodPost, "/api/extensions/install", map[string]string{"source": "edelweiss/hello@v0.1.0", "id": "hello", "sha256": helloSum})
	if resp.StatusCode != 201 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var view struct {
		ID, Version string
		Source      struct{ Type, Repo, Tag, Asset, SHA256 string }
	}
	_ = json.NewDecoder(resp.Body).Decode(&view)
	if view.ID != "hello" || view.Version != "0.1.0" || view.Source.Type != "github" || view.Source.Tag != "v0.1.0" {
		t.Fatalf("view %+v", view)
	}
	if f.requests[0] != "install edelweiss/hello@v0.1.0 hello "+helloSum {
		t.Fatalf("requests %v", f.requests)
	}
	if logs := env.logs.String(); !strings.Contains(logs, `msg="extension installed" id=hello version=0.1.0 repo=edelweiss/hello tag=v0.1.0`) ||
		!strings.Contains(logs, "user=alice") {
		t.Fatalf("no audit line:\n%s", logs)
	}
}

func TestUpdatesAndUpdateRoutes(t *testing.T) {
	f := &fakeInstaller{
		updates: func() ([]extmgr.Update, []extmgr.UpdateError) {
			return []extmgr.Update{{ID: "hello", Installed: "0.1.0", Available: "0.2.0", Repo: "edelweiss/hello", Tag: "v0.2.0",
					Asset: "edelweiss-ext-hello_0.2.0_linux_amd64.tar.gz", SHA256: helloSum}},
				[]extmgr.UpdateError{
					{ID: "docker", Err: fmt.Errorf("%w: acme/docker", github.ErrReleaseNotFound)},
					{ID: "broken", Err: errors.New("disk on fire")},
				}
		},
		update: func(id, _ string) (*extmgr.Extension, error) {
			if id != "hello" {
				return nil, extmgr.ErrNotFound
			}
			return helloExt("0.2.0"), nil
		},
	}
	env, alice := newInstallEnv(t, f)

	resp := alice.do(http.MethodGet, "/api/extensions/updates", nil)
	var body struct {
		Updates []extmgr.Update
		Errors  []struct{ ID, Code, Message string }
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != 200 || len(body.Updates) != 1 || body.Updates[0].Available != "0.2.0" ||
		len(body.Errors) != 2 || body.Errors[0].ID != "docker" || body.Errors[0].Code != "release_not_found" || !strings.Contains(body.Errors[0].Message, "acme/docker") {
		t.Fatalf("updates: %d %+v", resp.StatusCode, body)
	}
	// an unexpected failure is logged; the client only gets the generic message
	if e := body.Errors[1]; e.ID != "broken" || e.Code != "internal_error" || e.Message != "the update check failed" {
		t.Fatalf("internal error entry: %+v", e)
	}
	if logs := env.logs.String(); !strings.Contains(logs, `msg="extension update check failed" id=broken err="disk on fire"`) {
		t.Fatalf("internal error not logged:\n%s", logs)
	}

	resp = alice.do(http.MethodPost, "/api/extensions/hello/update", nil)
	var view struct{ ID, Version string }
	_ = json.NewDecoder(resp.Body).Decode(&view)
	if resp.StatusCode != 200 || view.Version != "0.2.0" {
		t.Fatalf("update: %d %+v", resp.StatusCode, view)
	}
	if f.requests[len(f.requests)-1] != "update hello " { // no body: nothing was confirmed
		t.Fatalf("requests %v", f.requests)
	}
	if resp := alice.do(http.MethodPost, "/api/extensions/nope/update", nil); resp.StatusCode != 404 || errorCode(t, resp) != "extension_not_found" {
		t.Fatalf("unknown: %d", resp.StatusCode)
	}
	if logs := env.logs.String(); !strings.Contains(logs, `msg="extension updated" id=hello version=0.2.0`) {
		t.Fatalf("no audit line:\n%s", logs)
	}
}

func TestUpdateRoutePassesTheConfirmedChecksum(t *testing.T) {
	f := &fakeInstaller{update: func(_, sha string) (*extmgr.Extension, error) {
		if sha == "x" {
			return nil, fmt.Errorf("%w: hello now has another SHA-256", extmgr.ErrReleaseChanged)
		}
		return helloExt("0.2.0"), nil
	}}
	env, alice := newInstallEnv(t, f)

	resp := alice.do(http.MethodPost, "/api/extensions/hello/update", map[string]string{"sha256": "x"})
	if resp.StatusCode != 409 || errorCode(t, resp) != "release_changed" {
		t.Fatalf("changed release: %d", resp.StatusCode)
	}
	if logs := env.logs.String(); strings.Contains(logs, `msg="extension updated"`) {
		t.Fatalf("audit line for a refused update:\n%s", logs)
	}

	resp = alice.do(http.MethodPost, "/api/extensions/hello/update", map[string]string{"sha256": helloSum})
	if resp.StatusCode != 200 {
		t.Fatalf("confirmed update: %d", resp.StatusCode)
	}

	// a body that is not a JSON object is refused before the installer is asked
	if resp := alice.do(http.MethodPost, "/api/extensions/hello/update", "oops"); resp.StatusCode != 400 || errorCode(t, resp) != "invalid_json" {
		t.Fatalf("bad body: %d", resp.StatusCode)
	}
	if want := []string{"update hello x", "update hello " + helloSum}; strings.Join(f.requests, "|") != strings.Join(want, "|") {
		t.Fatalf("requests %v, want %v", f.requests, want)
	}
}

func TestInstallerUnavailable(t *testing.T) {
	env := newTestEnv(t, nil)
	alice := &client{t: t, app: env.app}
	enableAdmin(t, alice)
	if resp := alice.do(http.MethodGet, "/api/extensions/updates", nil); resp.StatusCode != 503 || errorCode(t, resp) != "installer_unavailable" {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

// inHandler runs fn as a Fiber handler, with a real fasthttp request context.
func inHandler(t *testing.T, fn func(c fiber.Ctx)) {
	t.Helper()
	app := fiber.New()
	app.Get("/", func(c fiber.Ctx) error { fn(c); return c.SendStatus(http.StatusNoContent) })
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/", nil), fiber.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

// requestContext takes the cancellation of the fasthttp request context but none of its values: that context is
// recycled when the handler returns, while the GitHub client's dial goroutines may still read the values of the
// context they were given.
func TestRequestContextCarriesNoValues(t *testing.T) {
	type key struct{}
	var ctx context.Context
	var cancel context.CancelFunc
	inHandler(t, func(c fiber.Ctx) {
		c.RequestCtx().SetUserValue(key{}, "set")
		if c.RequestCtx().Value(key{}) != "set" {
			t.Error("premise: the request context carries the value")
		}
		ctx, cancel = requestContext(c, time.Hour)
		// while the request is running: a context derived from the request context would show the value
		if v := ctx.Value(key{}); v != nil {
			t.Errorf("the context carries a value of the request context: %v", v)
		}
	})
	defer cancel()
	if v := ctx.Value(key{}); v != nil {
		t.Fatalf("the context carries a value of the request context after the request: %v", v)
	}
	if dl, ok := ctx.Deadline(); !ok || time.Until(dl) < 59*time.Minute {
		t.Fatalf("deadline %v %v", dl, ok)
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("ended already: %v", err)
	}
	cancel()
	select {
	case <-ctx.Done():
	default:
		t.Fatal("cancel did not end the context")
	}
	if err := ctx.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
}

func TestRequestContextTimesOut(t *testing.T) {
	var ctx context.Context
	var cancel context.CancelFunc
	inHandler(t, func(c fiber.Ctx) { ctx, cancel = requestContext(c, 20*time.Millisecond) })
	defer cancel()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the context did not end at its deadline")
	}
	if err := ctx.Err(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
}

// The goroutine that watches for the shutdown ends with the context: neither cancel nor the deadline leaves one behind.
func TestRequestContextLeavesNoGoroutines(t *testing.T) {
	const n = 30
	var before int
	var cancels []context.CancelFunc
	inHandler(t, func(c fiber.Ctx) {
		before = runtime.NumGoroutine()
		for range n {
			_, cancel := requestContext(c, time.Hour)
			cancels = append(cancels, cancel)
		}
		for range n {
			_, cancel := requestContext(c, time.Millisecond) // ends by its deadline
			cancels = append(cancels, cancel)
		}
	})
	for _, cancel := range cancels {
		cancel()
	}
	// the request's own goroutines end meanwhile, so this can only be too lenient by a few
	limit := before + 5
	for end := time.Now().Add(5 * time.Second); runtime.NumGoroutine() > limit && time.Now().Before(end); {
		time.Sleep(10 * time.Millisecond)
	}
	if now := runtime.NumGoroutine(); now > limit {
		t.Fatalf("%d goroutines, %d before the %d contexts", now, before, 2*n)
	}
}

func TestMissingPackagesAnswer(t *testing.T) {
	missing := &extmgr.MissingPackagesError{Packages: []string{"docker", "cmd:docker-compose"}}
	f := &fakeInstaller{
		install: func(extmgr.InstallRequest) (*extmgr.Extension, error) { return nil, missing },
		update:  func(string, string) (*extmgr.Extension, error) { return nil, fmt.Errorf("update: %w", missing) },
	}
	_, alice := newInstallEnv(t, f)
	for _, r := range []struct {
		path string
		body any
	}{
		{"/api/extensions/install", map[string]string{"source": "a/b"}},
		{"/api/extensions/hello/update", map[string]string{"sha256": helloSum}},
	} {
		resp := alice.do(http.MethodPost, r.path, r.body)
		var body struct {
			Error struct {
				Code, Message string
				Packages      []string `json:"packages"`
			}
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		if resp.StatusCode != 422 || body.Error.Code != "missing_packages" ||
			!slices.Equal(body.Error.Packages, []string{"docker", "cmd:docker-compose"}) ||
			!strings.Contains(body.Error.Message, "missing packages: docker, cmd:docker-compose") {
			t.Errorf("%s: %d %+v", r.path, resp.StatusCode, body.Error)
		}
	}
}

// missingRegistry is the test registry plus an extension "docker" whose package "docker" is not installed.
type missingRegistry struct {
	Registry
	docker *extmgr.Extension
}

func (r missingRegistry) List() []*extmgr.Extension { return append(r.Registry.List(), r.docker) }

func (r missingRegistry) MissingPackages(e *extmgr.Extension) []string {
	if e == r.docker {
		return []string{"docker"}
	}
	return r.Registry.MissingPackages(e)
}

func TestListShowsRequiredAndMissingPackages(t *testing.T) {
	docker := &extmgr.Extension{
		Manifest: &manifest.Manifest{ID: "docker", Version: "0.1.0", APIVersion: 1, Name: manifest.Localized{"en": "Docker"},
			Menu: manifest.Menu{Section: "containers", Order: 10}, Requires: &manifest.Requires{APK: []string{"docker"}}},
		Source: extmgr.Source{Type: "file", Location: "/tmp/docker"},
	}
	env := newTestEnv(t, func(d *Deps) { d.Registry = missingRegistry{Registry: d.Registry, docker: docker} })
	alice := &client{t: t, app: env.app}
	if resp, _ := alice.login("alice", testPassword); resp.StatusCode != 200 {
		t.Fatal("login")
	}
	resp := alice.do(http.MethodGet, "/api/extensions", nil)
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	// testext requires nothing: both fields are empty arrays, not null
	if !strings.Contains(string(raw), `"requiresAdmin":false,"requires":{"apk":[]},"missingPackages":[]`) {
		t.Fatalf("testext entry: %s", raw)
	}
	if !strings.Contains(string(raw), `"id":"docker"`) ||
		!strings.Contains(string(raw), `"requires":{"apk":["docker"]},"missingPackages":["docker"]`) {
		t.Fatalf("docker entry: %s", raw)
	}
}
