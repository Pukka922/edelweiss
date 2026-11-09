package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Pukka922/edelweiss/core/internal/github"
	"github.com/Pukka922/edelweiss/core/internal/selfupdate"
)

type fakeSelfUpdate struct {
	mu         sync.Mutex
	about      selfupdate.About
	view       selfupdate.CheckView
	checkErr   error
	offer      selfupdate.Offer
	startErr   error
	dismissErr error
	calls      []string
}

func (f *fakeSelfUpdate) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeSelfUpdate) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeSelfUpdate) About() selfupdate.About {
	f.record("about")
	return f.about
}

func (f *fakeSelfUpdate) Check(_ context.Context, refresh bool) (selfupdate.CheckView, error) {
	f.record(fmt.Sprintf("check %v", refresh))
	return f.view, f.checkErr
}

func (f *fakeSelfUpdate) Start(_ context.Context, confirmed string) (selfupdate.Offer, error) {
	f.record("start " + confirmed)
	return f.offer, f.startErr
}

func (f *fakeSelfUpdate) Dismiss() error {
	f.record("dismiss")
	return f.dismissErr
}

var coreSum = strings.Repeat("cd", 32)

func newAboutFake() *fakeSelfUpdate {
	off := selfupdate.Offer{Current: "0.3.0", Latest: "0.3.1", Available: true, Release: "v0.3.1",
		Asset: "edelweissd_0.3.1_linux_amd64", Size: 42, SHA256: coreSum}
	return &fakeSelfUpdate{
		about: selfupdate.About{Version: "0.3.0", Arch: "amd64", Mode: selfupdate.Sys, Possible: true, Repo: "Pukka922/edelweiss"},
		view:  selfupdate.CheckView{Offer: off},
		offer: off,
	}
}

func TestAboutRoutesRights(t *testing.T) {
	f := newAboutFake()
	env := newTestEnv(t, func(d *Deps) { d.SelfUpdate = f })

	anon := &client{t: t, app: env.app}
	if resp := anon.do(http.MethodGet, "/api/about", nil); resp.StatusCode != 401 || errorCode(t, resp) != "unauthenticated" {
		t.Fatalf("GET /api/about without a session: %d", resp.StatusCode)
	}

	bob := &client{t: t, app: env.app}   // no admin rights
	alice := &client{t: t, app: env.app} // admin rights, admin mode off
	bob.login("bob", testPassword)
	alice.login("alice", testPassword)
	for _, c := range []*client{bob, alice} {
		resp := c.do(http.MethodGet, "/api/about", nil)
		var a struct {
			Version  string `json:"version"`
			Repo     string `json:"repo"`
			Possible bool   `json:"possible"`
		}
		if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&a) != nil || a.Version != "0.3.0" || a.Repo != "Pukka922/edelweiss" || !a.Possible {
			t.Fatalf("GET /api/about: %d %+v", resp.StatusCode, a)
		}
		for _, r := range []struct {
			method, path string
			body         any
		}{
			{http.MethodGet, "/api/about/update", nil},
			{http.MethodGet, "/api/about/update?refresh=1", nil},
			{http.MethodPost, "/api/about/update", map[string]string{"sha256": coreSum}},
			{http.MethodDelete, "/api/about/update/result", nil},
		} {
			if resp := c.do(r.method, r.path, r.body); resp.StatusCode != 403 || errorCode(t, resp) != "admin_required" {
				t.Errorf("%s %s without admin mode: %d", r.method, r.path, resp.StatusCode)
			}
		}
	}

	admin := &client{t: t, app: env.app}
	enableAdmin(t, admin)
	token := admin.csrf
	admin.csrf = ""
	if resp := admin.do(http.MethodPost, "/api/about/update", map[string]string{"sha256": coreSum}); resp.StatusCode != 403 || errorCode(t, resp) != "csrf_invalid" {
		t.Errorf("POST without CSRF: %d", resp.StatusCode)
	}
	if resp := admin.do(http.MethodDelete, "/api/about/update/result", nil); resp.StatusCode != 403 || errorCode(t, resp) != "csrf_invalid" {
		t.Errorf("DELETE without CSRF: %d", resp.StatusCode)
	}
	if got := f.called(); slices.ContainsFunc(got, func(s string) bool { return s != "about" }) {
		t.Fatalf("the updater was called: %q", got)
	}

	admin.csrf = token
	if resp := admin.do(http.MethodGet, "/api/about/update", nil); resp.StatusCode != 200 {
		t.Errorf("check: %d", resp.StatusCode)
	}
	resp := admin.do(http.MethodGet, "/api/about/update?refresh=1", nil)
	var v struct {
		Offer struct {
			Latest string `json:"latest"`
			SHA256 string `json:"sha256"`
		} `json:"offer"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&v) != nil || v.Offer.Latest != "0.3.1" || v.Offer.SHA256 != coreSum {
		t.Errorf("check with refresh: %d %+v", resp.StatusCode, v)
	}
	if resp := admin.do(http.MethodDelete, "/api/about/update/result", nil); resp.StatusCode != 204 {
		t.Errorf("dismiss: %d", resp.StatusCode)
	}
	if got := f.called(); !slices.Equal(got[len(got)-3:], []string{"check false", "check true", "dismiss"}) {
		t.Fatalf("calls = %q", got)
	}
}

func TestAboutUpdateErrors(t *testing.T) {
	f := newAboutFake()
	env := newTestEnv(t, func(d *Deps) { d.SelfUpdate = f })
	admin := &client{t: t, app: env.app}
	enableAdmin(t, admin)
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{&selfupdate.NotPossibleError{Reason: selfupdate.ReasonNotService}, 409, "not_possible"},
		{selfupdate.ErrNoUpdate, 409, "no_update"},
		{selfupdate.ErrBusy, 409, "operation_in_progress"},
		{fmt.Errorf("%w: %w", selfupdate.ErrBusy, selfupdate.ErrHelperRunning), 409, "operation_in_progress"},
		{selfupdate.ErrHelperRunning, 409, "operation_in_progress"},
		{fmt.Errorf("%w: x", selfupdate.ErrReleaseChanged), 409, "release_changed"},
		{fmt.Errorf("%w: x", selfupdate.ErrChecksumMismatch), 422, "checksum_mismatch"},
		{fmt.Errorf("%w: x", selfupdate.ErrWrongVersion), 422, "wrong_version"},
		{fmt.Errorf("%w: x", selfupdate.ErrNotRunnable), 422, "not_runnable"},
		{fmt.Errorf("%w: x", selfupdate.ErrBadScript), 422, "bad_script"},
		{fmt.Errorf("%w: x", selfupdate.ErrNoSpace), 409, "no_space"},
		{fmt.Errorf("%w: x", selfupdate.ErrNoAsset), 404, "no_asset"},
		{fmt.Errorf("%w: x", selfupdate.ErrNoChecksums), 422, "no_checksums"},
		{fmt.Errorf("%w: x", selfupdate.ErrChecksumsDisagree), 422, "checksums_disagree"},
		{fmt.Errorf("%w: x", github.ErrTooLarge), 413, "asset_too_large"},
		{fmt.Errorf("%w: x", github.ErrReleaseNotFound), 404, "release_not_found"},
		{fmt.Errorf("%w: x", github.ErrRateLimited), 503, "github_rate_limited"},
		{fmt.Errorf("%w: x", github.ErrUnavailable), 502, "github_unavailable"},
		{fmt.Errorf("%w: %w", github.ErrUnavailable, context.DeadlineExceeded), 504, "timeout"},
		{context.DeadlineExceeded, 504, "timeout"},
		{context.Canceled, 504, "timeout"},
		{errors.New("disk on fire"), 500, "internal_error"},
	} {
		f.startErr, f.checkErr = tc.err, tc.err
		for _, r := range []struct {
			method, path string
			body         any
		}{
			{http.MethodPost, "/api/about/update", map[string]string{"sha256": coreSum}},
			{http.MethodGet, "/api/about/update", nil},
		} {
			resp := admin.do(r.method, r.path, r.body)
			var body struct {
				Error struct{ Code, Message, Reason string }
			}
			_ = json.NewDecoder(resp.Body).Decode(&body)
			if resp.StatusCode != tc.status || body.Error.Code != tc.code {
				t.Errorf("%s %v: got %d %s, want %d %s", r.method, tc.err, resp.StatusCode, body.Error.Code, tc.status, tc.code)
			}
			if want := map[bool]string{true: "not_service"}[tc.code == "not_possible"]; body.Error.Reason != want {
				t.Errorf("%s %v: reason %q, want %q", r.method, tc.err, body.Error.Reason, want)
			}
			if tc.status == 500 && strings.Contains(body.Error.Message, "disk on fire") {
				t.Errorf("%s: message %q must be the generic one", r.method, body.Error.Message)
			}
		}
	}
	if !strings.Contains(env.logs.String(), "disk on fire") {
		t.Errorf("the internal error was not logged:\n%s", env.logs.String())
	}
	f.dismissErr = errors.New("read-only file system")
	if resp := admin.do(http.MethodDelete, "/api/about/update/result", nil); resp.StatusCode != 500 || errorCode(t, resp) != "internal_error" {
		t.Errorf("dismiss failure: %d", resp.StatusCode)
	}
}

func TestAboutAudit(t *testing.T) {
	f := newAboutFake()
	env := newTestEnv(t, func(d *Deps) { d.SelfUpdate = f })
	admin := &client{t: t, app: env.app}
	enableAdmin(t, admin)

	if resp := admin.do(http.MethodPost, "/api/about/update", nil); resp.StatusCode != 400 || errorCode(t, resp) != "invalid_json" {
		t.Errorf("no body: %d", resp.StatusCode)
	}
	f.startErr = selfupdate.ErrBusy
	if resp := admin.do(http.MethodPost, "/api/about/update", map[string]string{"sha256": coreSum}); resp.StatusCode != 409 {
		t.Errorf("busy: %d", resp.StatusCode)
	}
	if strings.Contains(env.logs.String(), "self-update started") {
		t.Fatalf("an update that did not start is in the audit log:\n%s", env.logs.String())
	}

	f.startErr = nil
	resp := admin.do(http.MethodPost, "/api/about/update", map[string]string{"sha256": coreSum})
	var got map[string]any
	if resp.StatusCode != 202 || json.NewDecoder(resp.Body).Decode(&got) != nil || len(got) != 1 || got["to"] != "0.3.1" {
		t.Fatalf("start: %d %v", resp.StatusCode, got)
	}
	if calls := f.called(); calls[len(calls)-1] != "start "+coreSum {
		t.Fatalf("calls = %q", calls)
	}
	want := `msg="self-update started" user=alice from=0.3.0 to=0.3.1 asset=edelweissd_0.3.1_linux_amd64 sha256=` + coreSum
	if !strings.Contains(env.logs.String(), want) {
		t.Fatalf("no %q in the log:\n%s", want, env.logs.String())
	}
}

// The update needs the SHA-256 the user confirmed: without one it is refused before the updater is asked.
func TestAboutUpdateNeedsTheChecksum(t *testing.T) {
	f := newAboutFake()
	env := newTestEnv(t, func(d *Deps) { d.SelfUpdate = f })
	admin := &client{t: t, app: env.app}
	enableAdmin(t, admin)
	for _, body := range []any{map[string]string{}, map[string]string{"sha256": ""}, map[string]any{"sha256": nil}} {
		if resp := admin.do(http.MethodPost, "/api/about/update", body); resp.StatusCode != 400 || errorCode(t, resp) != "invalid_input" {
			t.Errorf("%v: %d", body, resp.StatusCode)
		}
	}
	if got := f.called(); slices.ContainsFunc(got, func(s string) bool { return strings.HasPrefix(s, "start") }) {
		t.Fatalf("the updater was asked: %q", got)
	}
}

// That lbu did not save the update is for everybody (the page says so after the sign-in, outside admin mode); the raw
// lbu output is admin information.
func TestAboutHidesTheLbuErrorOutsideAdminMode(t *testing.T) {
	f := newAboutFake()
	f.about.State.Result = &selfupdate.Result{From: "0.3.0", To: "0.3.1", Outcome: "updated", LbuError: "lbu: /media/usb is read-only"}
	env := newTestEnv(t, func(d *Deps) { d.SelfUpdate = f })
	result := func(c *client) map[string]any {
		t.Helper()
		var a struct {
			State struct {
				Result map[string]any `json:"result"`
			} `json:"state"`
		}
		resp := c.do(http.MethodGet, "/api/about", nil)
		if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&a) != nil {
			t.Fatalf("GET /api/about: %d", resp.StatusCode)
		}
		return a.State.Result
	}
	alice := &client{t: t, app: env.app}
	alice.login("alice", testPassword) // admin rights, admin mode off
	if r := result(alice); r["outcome"] != "updated" || r["to"] != "0.3.1" || r["lbuError"] != nil || r["lbuFailed"] != true {
		t.Errorf("without admin mode: %v", r)
	}
	enableAdmin(t, alice)
	if r := result(alice); r["lbuError"] != "lbu: /media/usb is read-only" || r["lbuFailed"] != true {
		t.Errorf("in admin mode: %v", r)
	}
	if f.about.State.Result.LbuError == "" || f.about.State.Result.LbuFailed {
		t.Error("the updater's result was changed")
	}
}

func TestSelfUpdateUnavailable(t *testing.T) {
	env := newTestEnv(t, nil)
	admin := &client{t: t, app: env.app}
	enableAdmin(t, admin)
	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/api/about"},
		{http.MethodGet, "/api/about/update"},
		{http.MethodDelete, "/api/about/update/result"},
	} {
		if resp := admin.do(r.method, r.path, nil); resp.StatusCode != 503 || errorCode(t, resp) != "self_update_unavailable" {
			t.Errorf("%s %s without an updater: %d", r.method, r.path, resp.StatusCode)
		}
	}
}
