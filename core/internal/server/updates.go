package server

import (
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/Pukka922/edelweiss/core/internal/extmgr"
)

// updatesMaxAge is how long an update check is kept: GitHub allows 60 requests an hour without a token, and every
// check asks once per extension installed from GitHub.
const updatesMaxAge = 2 * time.Minute

// updatesView is the answer of GET /api/extensions/updates.
type updatesView struct {
	Updates   []extmgr.Update   `json:"updates"`
	Errors    []updateErrorView `json:"errors"`
	CheckedAt time.Time         `json:"checkedAt"`
	// AgeSeconds is the age of the check by the clock of the core (Deps.Now minus CheckedAt). The clock of a browser
	// may be wrong, so the page counts on from this age instead of comparing CheckedAt with its own clock.
	AgeSeconds int64 `json:"ageSeconds"`
	// Stale: GitHub did not answer and this is the last good check. StaleCode and StaleReason are the code and the
	// message of the first failure that concerned the whole check.
	Stale       bool   `json:"stale"`
	StaleCode   string `json:"staleCode,omitempty"`
	StaleReason string `json:"staleReason,omitempty"`
}

// updatesCache keeps the last good update check for the extensions it was made for.
type updatesCache struct {
	mu   sync.Mutex
	key  string
	view *updatesView
}

func (u *updatesCache) get(key string) *updatesView {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.view == nil || u.key != key {
		return nil
	}
	v := *u.view
	return &v
}

func (u *updatesCache) put(key string, v updatesView) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.key, u.view = key, &v
}

// updatesKey names the extensions an update check is about: those installed from GitHub, with version and repository.
func (s *Server) updatesKey() string {
	var parts []string
	for _, e := range s.d.Registry.List() {
		if !e.Dev && e.Source.Type == "github" {
			parts = append(parts, e.Manifest.ID+"@"+e.Manifest.Version+"@"+e.Source.Repo)
		}
	}
	slices.Sort(parts)
	return strings.Join(parts, ",")
}

// wideCodes are the failures that concern the whole check, not one extension: GitHub does not answer (rate limit,
// outage) or the check ran out of time. What the check found besides is incomplete then.
var wideCodes = []string{"github_rate_limited", "github_unavailable", "timeout"}

// wideError returns the first failure of a check that concerns the whole check. A result with one is not kept, and
// it does not replace the last good result.
func wideError(errs []updateErrorView) (updateErrorView, bool) {
	for _, e := range errs {
		if slices.Contains(wideCodes, e.Code) {
			return e, true
		}
	}
	return updateErrorView{}, false
}

// replyUpdates sends v with its age.
func (s *Server) replyUpdates(c fiber.Ctx, v updatesView) error {
	v.AgeSeconds = max(0, int64(s.now().Sub(v.CheckedAt)/time.Second))
	return c.JSON(v)
}

func (s *Server) now() time.Time {
	if s.d.Now != nil {
		return s.d.Now()
	}
	return time.Now()
}
