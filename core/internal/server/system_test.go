package server

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

// powerRecorder is a Power that records its actions. The tests never run a real reboot or poweroff: Deps.Power is
// always a recorder.
type powerRecorder struct {
	actions chan string
	err     error
}

func newPowerRecorder(err error) *powerRecorder {
	return &powerRecorder{actions: make(chan string, 8), err: err}
}

func (p *powerRecorder) run(action string) error {
	p.actions <- action
	return p.err
}

// next waits for the next action the recorder was asked to run.
func (p *powerRecorder) next(t *testing.T) string {
	t.Helper()
	select {
	case a := <-p.actions:
		return a
	case <-time.After(2 * time.Second):
		t.Fatal("the power function was not run")
		return ""
	}
}

// none reports whether the recorder was asked to run anything so far.
func (p *powerRecorder) none() bool { return len(p.actions) == 0 }

func TestPowerNeedsASessionCSRFAndAdminModeAndRunsTheAction(t *testing.T) {
	for _, action := range []string{"reboot", "poweroff"} {
		t.Run(action, func(t *testing.T) {
			rec := newPowerRecorder(nil)
			env := newTestEnv(t, func(d *Deps) { d.Power = rec.run })
			path := "/api/system/" + action
			alice := &client{t: t, app: env.app}

			if resp := alice.do(http.MethodPost, path, nil); resp.StatusCode != 401 || errorCode(t, resp) != "unauthenticated" {
				t.Fatalf("without a session: %d", resp.StatusCode)
			}
			if resp, _ := alice.login("alice", testPassword); resp.StatusCode != 200 {
				t.Fatal("login")
			}
			if resp := alice.do(http.MethodPost, path, nil); resp.StatusCode != 403 || errorCode(t, resp) != "admin_required" {
				t.Fatalf("without admin mode: %d", resp.StatusCode)
			}
			enableAdmin(t, alice)
			csrf := alice.csrf
			alice.csrf = ""
			if resp := alice.do(http.MethodPost, path, nil); resp.StatusCode != 403 || errorCode(t, resp) != "csrf_invalid" {
				t.Fatalf("without the CSRF token: %d", resp.StatusCode)
			}
			alice.csrf = csrf
			if !rec.none() {
				t.Fatal("a rejected request ran the action")
			}

			resp := alice.do(http.MethodPost, path, nil)
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != 202 || string(body) != `{"action":"`+action+`"}` {
				t.Fatalf("%d %s", resp.StatusCode, body)
			}
			if got := rec.next(t); got != action {
				t.Fatalf("ran %q, want %q", got, action)
			}
			if !strings.Contains(env.logs.String(), "host "+action+" requested") {
				t.Fatalf("the request was not logged:\n%s", env.logs)
			}
		})
	}
}

func TestPowerAnswersBeforeItRunsTheAction(t *testing.T) {
	rec := newPowerRecorder(nil)
	env := newTestEnv(t, func(d *Deps) {
		d.Power = rec.run
		d.PowerDelay = 500 * time.Millisecond
	})
	alice := &client{t: t, app: env.app}
	enableAdmin(t, alice)

	if resp := alice.do(http.MethodPost, "/api/system/reboot", nil); resp.StatusCode != 202 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if !rec.none() {
		t.Fatal("the action ran before the answer was sent")
	}
	if got := rec.next(t); got != "reboot" {
		t.Fatalf("ran %q", got)
	}
}

func TestPowerLogsAFailureOfTheAction(t *testing.T) {
	rec := newPowerRecorder(errors.New("no such command"))
	env := newTestEnv(t, func(d *Deps) { d.Power = rec.run })
	alice := &client{t: t, app: env.app}
	enableAdmin(t, alice)

	if resp := alice.do(http.MethodPost, "/api/system/poweroff", nil); resp.StatusCode != 202 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	rec.next(t)
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(env.logs.String(), "host poweroff failed") {
		if time.Now().After(deadline) {
			t.Fatalf("the failure was not logged:\n%s", env.logs)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(env.logs.String(), "no such command") {
		t.Fatalf("the log does not say why:\n%s", env.logs)
	}
}

func TestPowerWithoutARunnerIsUnavailable(t *testing.T) {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil))) // the error handler logs the 503
	t.Cleanup(func() { slog.SetDefault(prev) })

	env := newTestEnv(t, nil)
	alice := &client{t: t, app: env.app}
	enableAdmin(t, alice)
	for _, action := range []string{"reboot", "poweroff"} {
		if resp := alice.do(http.MethodPost, "/api/system/"+action, nil); resp.StatusCode != 503 || errorCode(t, resp) != "power_unavailable" {
			t.Fatalf("%s: status %d, want 503 power_unavailable", action, resp.StatusCode)
		}
	}
}

// bob is not in the admin group: he cannot enter admin mode, so he can never reach the power routes (and the
// request does not run anything).
func TestPowerIsRefusedToAUserOutsideTheAdminGroup(t *testing.T) {
	rec := newPowerRecorder(nil)
	env := newTestEnv(t, func(d *Deps) { d.Power = rec.run })
	bob := &client{t: t, app: env.app}
	if resp, s := bob.login("bob", testPassword); resp.StatusCode != 200 || s.CanAdmin {
		t.Fatalf("login: %d %+v", resp.StatusCode, s)
	}
	if resp := bob.do(http.MethodPost, "/api/session/admin", map[string]string{"password": testPassword}); resp.StatusCode != 403 || errorCode(t, resp) != "admin_not_permitted" {
		t.Fatalf("admin mode: %d", resp.StatusCode)
	}
	for _, action := range []string{"reboot", "poweroff"} {
		if resp := bob.do(http.MethodPost, "/api/system/"+action, nil); resp.StatusCode != 403 || errorCode(t, resp) != "admin_required" {
			t.Fatalf("%s: %d", action, resp.StatusCode)
		}
	}
	time.Sleep(50 * time.Millisecond) // an action that was started wrongly would be run at once (no PowerDelay)
	if !rec.none() {
		t.Fatalf("a refused request ran %q", <-rec.actions)
	}
}
