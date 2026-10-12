package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Pukka922/edelweiss/core/internal/extmgr"
	"github.com/Pukka922/edelweiss/core/internal/github"
)

type updatesBody struct {
	Updates     []extmgr.Update                      `json:"updates"`
	Errors      []struct{ ID, Code, Message string } `json:"errors"`
	CheckedAt   time.Time                            `json:"checkedAt"`
	AgeSeconds  int64                                `json:"ageSeconds"`
	Stale       bool                                 `json:"stale"`
	StaleCode   string                               `json:"staleCode"`
	StaleReason string                               `json:"staleReason"`
}

// setUpdates makes the fake's update check answer with updates and failed from now on.
func (f *fakeInstaller) setUpdates(updates []extmgr.Update, failed []extmgr.UpdateError) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = func() ([]extmgr.Update, []extmgr.UpdateError) { return updates, failed }
}

// updatesFake is a fakeInstaller whose update check answers with updates and failed.
func updatesFake(updates []extmgr.Update, failed []extmgr.UpdateError) *fakeInstaller {
	f := &fakeInstaller{}
	f.setUpdates(updates, failed)
	return f
}

// fakeClock is the clock of Deps.Now that a test moves.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// updatesEnv is newInstallEnv with the clock of the test; testext is installed from GitHub.
func updatesEnv(t *testing.T, f *fakeInstaller, clock *fakeClock) (*installEnv, *client) {
	t.Helper()
	return newInstallEnv(t, f, func(d *Deps) { d.Now = clock.Now })
}

func getUpdates(t *testing.T, c *client, path string) updatesBody {
	t.Helper()
	resp := c.do(http.MethodGet, path, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", path, resp.StatusCode)
	}
	var body updatesBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return body
}

const (
	checkPath   = "/api/extensions/updates"
	refreshPath = "/api/extensions/updates?refresh=1"
)

var testextUpdate = extmgr.Update{ID: "testext", Installed: "0.1.0", Available: "0.2.0"}

func TestUpdatesAreKeptForTwoMinutes(t *testing.T) {
	clock := newClock()
	f := updatesFake([]extmgr.Update{testextUpdate}, nil)
	env, c := updatesEnv(t, f, clock) // testext is installed from GitHub
	first := getUpdates(t, c, checkPath)
	if !first.CheckedAt.Equal(clock.Now()) || first.Stale || first.AgeSeconds != 0 {
		t.Fatalf("first check: %+v", first)
	}
	clock.advance(time.Minute)
	second := getUpdates(t, c, checkPath)
	if f.updateCalls != 1 || !second.CheckedAt.Equal(first.CheckedAt) || len(second.Updates) != 1 {
		t.Fatalf("calls %d, checked %v / %v", f.updateCalls, first.CheckedAt, second.CheckedAt)
	}
	if second.AgeSeconds != 60 { // the age is by the clock of the core
		t.Fatalf("age %d seconds, want 60", second.AgeSeconds)
	}
	getUpdates(t, c, refreshPath) // the button always asks GitHub
	if f.updateCalls != 2 {
		t.Fatalf("refresh: calls %d", f.updateCalls)
	}
	clock.advance(2*time.Minute + time.Second)
	getUpdates(t, c, checkPath)
	if f.updateCalls != 3 {
		t.Fatalf("after 2 minutes: calls %d", f.updateCalls)
	}
	env.installGitHubExtension("other", "0.1.0") // another set of extensions: checked again at once
	getUpdates(t, c, checkPath)
	if f.updateCalls != 4 {
		t.Fatalf("after a change of the extensions: calls %d", f.updateCalls)
	}
}

// Extensions that are not installed from GitHub are not checked, so they are not part of what a check is about.
func TestUpdatesIgnoreExtensionsThatAreNotFromGitHub(t *testing.T) {
	clock := newClock()
	f := updatesFake([]extmgr.Update{testextUpdate}, nil)
	env, c := updatesEnv(t, f, clock)
	getUpdates(t, c, checkPath)
	env.installLocalExtension("devext", "0.1.0", true)
	env.installLocalExtension("fileext", "0.1.0", false)
	got := getUpdates(t, c, checkPath)
	if f.updateCalls != 1 || len(got.Updates) != 1 {
		t.Fatalf("calls %d, got %+v", f.updateCalls, got)
	}
}

// What a check is about changes when an extension from GitHub is updated or removed, not only when one is added.
func TestUpdatesAreCheckedAgainAfterAnUpdateOrARemoval(t *testing.T) {
	clock := newClock()
	f := updatesFake([]extmgr.Update{}, nil)
	env, c := updatesEnv(t, f, clock)
	env.installGitHubExtension("other", "0.1.0")
	getUpdates(t, c, checkPath)
	getUpdates(t, c, checkPath)
	if f.updateCalls != 1 {
		t.Fatalf("kept: calls %d", f.updateCalls)
	}
	env.installGitHubExtension("other", "0.2.0") // an update
	getUpdates(t, c, checkPath)
	getUpdates(t, c, checkPath)
	if f.updateCalls != 2 {
		t.Fatalf("after an update: calls %d", f.updateCalls)
	}
	env.removeExtension("other")
	getUpdates(t, c, checkPath)
	getUpdates(t, c, checkPath)
	if f.updateCalls != 3 {
		t.Fatalf("after a removal: calls %d", f.updateCalls)
	}
}

func TestUpdatesShowTheLastResultWhenGitHubFails(t *testing.T) {
	clock := newClock()
	f := updatesFake([]extmgr.Update{testextUpdate}, nil)
	_, c := updatesEnv(t, f, clock)
	first := getUpdates(t, c, checkPath)
	clock.advance(5 * time.Minute)
	f.setUpdates([]extmgr.Update{}, []extmgr.UpdateError{{ID: "testext", Err: github.ErrRateLimited}})
	for range 2 { // the failed check did not replace the kept result: asking again shows the same
		got := getUpdates(t, c, refreshPath)
		if !got.Stale || got.StaleCode != "github_rate_limited" || got.StaleReason == "" || len(got.Updates) != 1 ||
			!got.CheckedAt.Equal(first.CheckedAt) || got.AgeSeconds != 300 {
			t.Fatalf("got %+v", got)
		}
	}
	// once GitHub answers again, the new result replaces it
	f.setUpdates([]extmgr.Update{}, nil)
	if got := getUpdates(t, c, refreshPath); got.Stale || got.StaleCode != "" || len(got.Updates) != 0 || !got.CheckedAt.Equal(clock.Now()) {
		t.Fatalf("after GitHub answered again: %+v", got)
	}
}

// A check that failed is not what the next request without refresh gets: that is the last good result, with its own time.
func TestAStaleAnswerLeavesTheKeptResultAlone(t *testing.T) {
	clock := newClock()
	f := updatesFake([]extmgr.Update{testextUpdate}, nil)
	_, c := updatesEnv(t, f, clock)
	first := getUpdates(t, c, checkPath)
	clock.advance(time.Minute)
	f.setUpdates([]extmgr.Update{}, []extmgr.UpdateError{{ID: "testext", Err: github.ErrRateLimited}})
	if got := getUpdates(t, c, refreshPath); !got.Stale {
		t.Fatalf("premise: stale answer, got %+v", got)
	}
	got := getUpdates(t, c, checkPath) // within the 2 minutes of the good check
	if f.updateCalls != 2 || got.Stale || len(got.Updates) != 1 || len(got.Errors) != 0 || !got.CheckedAt.Equal(first.CheckedAt) || got.AgeSeconds != 60 {
		t.Fatalf("calls %d, got %+v", f.updateCalls, got)
	}
}

// A failure that concerns the whole check (GitHub or the time) leaves what was found besides incomplete: the last good
// result is shown, not the partial one, and the failure is no good result to keep.
func TestUpdatesFallBackForEveryFailureOfTheWholeCheck(t *testing.T) {
	timedOut := fmt.Errorf("check: %w", context.DeadlineExceeded)
	for _, tc := range []struct {
		name    string
		updates []extmgr.Update
		failed  []extmgr.UpdateError
		code    string
	}{
		{"rate limit", nil, []extmgr.UpdateError{{ID: "testext", Err: github.ErrRateLimited}}, "github_rate_limited"},
		{"outage", nil, []extmgr.UpdateError{{ID: "testext", Err: github.ErrUnavailable}}, "github_unavailable"},
		{"timeout", nil, []extmgr.UpdateError{{ID: "testext", Err: timedOut}}, "timeout"},
		{"timeout after a partial result", []extmgr.Update{{ID: "other", Installed: "0.1.0", Available: "0.9.0"}},
			[]extmgr.UpdateError{{ID: "testext", Err: timedOut}}, "timeout"},
		{"a failure of one extension and then the rate limit", nil, []extmgr.UpdateError{
			{ID: "other", Err: github.ErrReleaseNotFound}, {ID: "testext", Err: github.ErrRateLimited}}, "github_rate_limited"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := newClock()
			f := updatesFake([]extmgr.Update{testextUpdate}, nil)
			_, c := updatesEnv(t, f, clock)
			first := getUpdates(t, c, checkPath)
			clock.advance(time.Minute)
			f.setUpdates(append([]extmgr.Update{}, tc.updates...), tc.failed)
			got := getUpdates(t, c, refreshPath)
			if !got.Stale || got.StaleCode != tc.code || got.StaleReason == "" ||
				len(got.Updates) != 1 || got.Updates[0].ID != "testext" || !got.CheckedAt.Equal(first.CheckedAt) {
				t.Fatalf("got %+v", got)
			}
			kept := getUpdates(t, c, checkPath)
			if f.updateCalls != 2 || kept.Stale || len(kept.Updates) != 1 || !kept.CheckedAt.Equal(first.CheckedAt) {
				t.Fatalf("after it: calls %d, got %+v", f.updateCalls, kept)
			}
		})
	}
}

// Without a good result to show, the errors are what the page gets, and the failed check is not kept.
func TestUpdatesWithoutAPreviousResultReportTheError(t *testing.T) {
	for _, tc := range []struct {
		name   string
		failed []extmgr.UpdateError
		codes  []string
	}{
		{"rate limit", []extmgr.UpdateError{{ID: "testext", Err: github.ErrRateLimited}}, []string{"github_rate_limited"}},
		{"timeout", []extmgr.UpdateError{{ID: "testext", Err: context.DeadlineExceeded}}, []string{"timeout"}},
		{"a failure of one extension and then the rate limit", []extmgr.UpdateError{
			{ID: "other", Err: github.ErrReleaseNotFound}, {ID: "testext", Err: github.ErrRateLimited}},
			[]string{"release_not_found", "github_rate_limited"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := updatesFake([]extmgr.Update{}, tc.failed)
			_, c := newInstallEnv(t, f)
			got := getUpdates(t, c, checkPath)
			var codes []string
			for _, e := range got.Errors {
				codes = append(codes, e.Code)
			}
			if got.Stale || fmt.Sprint(codes) != fmt.Sprint(tc.codes) || got.CheckedAt.IsZero() {
				t.Fatalf("got %+v", got)
			}
			getUpdates(t, c, checkPath)
			if f.updateCalls != 2 { // a failure is not kept: the next check asks again
				t.Fatalf("calls %d", f.updateCalls)
			}
		})
	}
}

// A failure that concerns one extension (a release that does not exist) says nothing about GitHub as a whole: the
// result is kept with it.
func TestUpdatesKeepAResultWithAnErrorOfAnExtension(t *testing.T) {
	f := updatesFake([]extmgr.Update{}, []extmgr.UpdateError{{ID: "testext", Err: github.ErrReleaseNotFound}})
	_, c := newInstallEnv(t, f)
	first := getUpdates(t, c, checkPath)
	second := getUpdates(t, c, checkPath)
	if f.updateCalls != 1 || len(second.Errors) != 1 || second.Errors[0].Code != "release_not_found" || !second.CheckedAt.Equal(first.CheckedAt) {
		t.Fatalf("calls %d, got %+v", f.updateCalls, second)
	}
}

// Without Deps.Now the update check is stamped with the real time.
func TestUpdatesUseTheRealClockByDefault(t *testing.T) {
	f := updatesFake([]extmgr.Update{}, nil)
	_, c := newInstallEnv(t, f)
	before := time.Now()
	got := getUpdates(t, c, checkPath)
	if got.CheckedAt.Before(before.Add(-time.Second)) || got.CheckedAt.After(time.Now().Add(time.Second)) {
		t.Fatalf("checked at %v, now %v", got.CheckedAt, time.Now())
	}
}
