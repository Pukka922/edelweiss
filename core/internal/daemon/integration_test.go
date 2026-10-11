//go:build integration

package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/Pukka922/edelweiss/core/internal/config"
	"github.com/Pukka922/edelweiss/core/internal/testutil"
)

func TestDaemonEndToEndAsRealUser(t *testing.T) {
	u := testutil.EnsureUser(t, "edeltest", "Edel-Pass-1", "wheel")
	base := startDaemon(t, config.Default()) // real /etc/shadow, root denied
	uid, _ := strconv.Atoi(u.Uid)
	if got := loginAndHello(t, base, "edeltest", "Edel-Pass-1"); got != uid {
		t.Fatalf("extension ran as uid %d, want %d", got, uid)
	}
}

// adminSession is the part of the session JSON the admin-mode test needs.
type adminSession struct {
	Admin     bool   `json:"admin"`
	CanAdmin  bool   `json:"canAdmin"`
	CSRFToken string `json:"csrfToken"`
}

// sessionOf returns the current session of an HTTP client logged in to base.
func sessionOf(t *testing.T, client *http.Client, base string) adminSession {
	t.Helper()
	resp, err := client.Get(base + "/api/session")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("session: %v %v", resp, err)
	}
	defer resp.Body.Close()
	var s adminSession
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// enableAdmin posts the admin-mode request (JSON body and CSRF header) and
// returns the response; the caller closes the body.
func enableAdmin(t *testing.T, client *http.Client, base, csrf, password string) *http.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"password": password})
	req, err := http.NewRequest(http.MethodPost, base+"/api/session/admin", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Csrf-Token", csrf)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("admin request: %v", err)
	}
	return resp
}

// helloUID asks testext (through the daemon) which OS uid it runs as.
func helloUID(t *testing.T, client *http.Client, base string) int {
	t.Helper()
	resp, err := client.Get(base + "/ext/testext/api/hello")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("hello: %v %v", resp, err)
	}
	defer resp.Body.Close()
	var hello struct {
		OSUid int `json:"osUid"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&hello); err != nil {
		t.Fatal(err)
	}
	return hello.OSUid
}

func TestDaemonAdminModeAsRealUser(t *testing.T) {
	const password = "Edel-Pass-1"
	wheel := testutil.EnsureUser(t, "edeltest", password, "wheel")
	plain := testutil.EnsureUser(t, "edelplain", password) // not in wheel
	base := startDaemon(t, config.Default())               // real /etc/shadow, real wheel group

	t.Run("wheel member gets a root extension process", func(t *testing.T) {
		wheelUID, _ := strconv.Atoi(wheel.Uid)
		client := login(t, base, "edeltest", password)
		sess := sessionOf(t, client, base)
		if !sess.CanAdmin || sess.Admin {
			t.Fatalf("before admin mode: %+v", sess)
		}
		if got := helloUID(t, client, base); got != wheelUID {
			t.Fatalf("extension ran as uid %d before admin mode, want %d", got, wheelUID)
		}

		// A wrong password must not enable admin mode.
		resp := enableAdmin(t, client, base, sess.CSRFToken, "wrong")
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("admin mode with wrong password: status %d", resp.StatusCode)
		}
		if got := helloUID(t, client, base); got != wheelUID {
			t.Fatalf("extension ran as uid %d after a failed admin attempt, want %d", got, wheelUID)
		}

		resp = enableAdmin(t, client, base, sess.CSRFToken, password)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("admin mode: status %d", resp.StatusCode)
		}
		var on adminSession
		if err := json.NewDecoder(resp.Body).Decode(&on); err != nil || !on.Admin {
			t.Fatalf("admin mode response: %+v %v", on, err)
		}
		if got := helloUID(t, client, base); got != 0 {
			t.Fatalf("extension ran as uid %d in admin mode, want 0", got)
		}
	})

	t.Run("user outside wheel is refused", func(t *testing.T) {
		plainUID, _ := strconv.Atoi(plain.Uid)
		client := login(t, base, "edelplain", password)
		sess := sessionOf(t, client, base)
		if sess.CanAdmin || sess.Admin {
			t.Fatalf("session of a user outside wheel: %+v", sess)
		}
		resp := enableAdmin(t, client, base, sess.CSRFToken, password)
		defer resp.Body.Close()
		var env struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&env)
		if resp.StatusCode != http.StatusForbidden || env.Error.Code != "admin_not_permitted" {
			t.Fatalf("admin mode outside wheel: status %d, code %q", resp.StatusCode, env.Error.Code)
		}
		if got := helloUID(t, client, base); got != plainUID {
			t.Fatalf("extension ran as uid %d, want %d", got, plainUID)
		}
	})
}
