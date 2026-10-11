// Package server builds the edelweissd HTTP application.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io/fs"
	"log/slog"
	"math"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/helmet"
	"github.com/gofiber/fiber/v3/middleware/recover"

	"github.com/Pukka922/edelweiss/core/internal/auth"
	"github.com/Pukka922/edelweiss/core/internal/config"
	"github.com/Pukka922/edelweiss/core/internal/extmgr"
	"github.com/Pukka922/edelweiss/core/internal/procman"
	"github.com/Pukka922/edelweiss/core/internal/session"
	"github.com/Pukka922/edelweiss/sdk"
)

// Authenticator is implemented by *auth.Authenticator.
type Authenticator interface {
	Login(name, password string) (*auth.UserInfo, error)
	Reauthenticate(u *auth.UserInfo, password string) error
	Refresh(name string) (*auth.UserInfo, error)
	CanAdmin(u *auth.UserInfo) bool
	Root() (*auth.UserInfo, error)
}

// Registry is implemented by *extmgr.Manager.
type Registry interface {
	List() []*extmgr.Extension
	Get(id string) (*extmgr.Extension, bool)
	Remove(id string) error
	// MissingPackages returns the Alpine packages e requires that are not installed (never nil).
	MissingPackages(e *extmgr.Extension) []string
}

// Installer is implemented by *extmgr.Installer.
type Installer interface {
	Resolve(ctx context.Context, source string) (*extmgr.Resolution, error)
	Install(ctx context.Context, req extmgr.InstallRequest) (*extmgr.Extension, error)
	// Updates returns a non-nil slice of updates (it is sent as a JSON array) and the extensions whose check failed.
	Updates(ctx context.Context) ([]extmgr.Update, []extmgr.UpdateError)
	// Update installs the latest release of id; confirmed is the SHA-256 the user saw in the update list
	// ("" skips the check; a mismatch is extmgr.ErrReleaseChanged).
	Update(ctx context.Context, id, confirmed string) (*extmgr.Extension, error)
}

// Pool is implemented by *procman.Manager.
type Pool interface {
	Acquire(ctx context.Context, spec procman.Spec) (*procman.Process, func(), error)
	StopUser(user string, adminOnly bool)
}

// Deps are the collaborators of the HTTP application.
type Deps struct {
	Config    *config.Config
	Auth      Authenticator
	Sessions  *session.Store
	Registry  Registry
	Installer Installer // GitHub installs and updates; nil answers 503 installer_unavailable
	Pool      Pool
	Shell     fs.FS
	Dev       bool // serve the shell from disk for development: index.html re-read per request, no immutable caching
	Version   string
	Logger    *slog.Logger
	Hostname  func() string
	Timeouts  Timeouts // zero fields: the defaults
}

// Timeouts bound how long a client may hold a connection without progress.
type Timeouts struct {
	// Read bounds the TLS handshake and the request head and, for uploads to
	// extensions, every pause in the body (default DefaultReadTimeout).
	Read time.Duration
	// Idle bounds the wait for the next request on a kept-alive connection
	// (default DefaultIdleTimeout).
	Idle time.Duration
}

const (
	DefaultReadTimeout = 30 * time.Second
	DefaultIdleTimeout = 120 * time.Second
	// MaxConnsPerIP is how many connections one client address may hold open
	// at once (see LimitListener).
	MaxConnsPerIP = 64
)

// Server holds the state shared by the HTTP handlers.
type Server struct {
	d     Deps
	shell *shell
}

const (
	cookieSession = "edelweiss_session"
	cookieLang    = "edelweiss_lang"
	headerCSRF    = "X-Csrf-Token"
	localSession  = "edelweiss.session"
	localReqID    = "edelweiss.requestID"
	apiBodyLimit  = 64 << 10
	// maxBody is the server-wide request body limit (uploads to extensions are
	// streamed); 64 GiB, capped to what int holds on 32-bit platforms.
	maxBody = min(64<<30, math.MaxInt)
)

// New builds the Fiber application.
func New(d Deps) (*fiber.App, error) {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Hostname == nil {
		d.Hostname = func() string { return "" }
	}
	if d.Timeouts.Read <= 0 {
		d.Timeouts.Read = DefaultReadTimeout
	}
	if d.Timeouts.Idle <= 0 {
		d.Timeouts.Idle = DefaultIdleTimeout
	}
	sh, err := newShell(d.Shell, d.Dev, d.Logger)
	if err != nil {
		return nil, err
	}
	s := &Server{d: d, shell: sh}

	app := fiber.New(fiber.Config{
		AppName:           "edelweiss",
		ErrorHandler:      sdk.ErrorHandler,
		StreamRequestBody: true,    // large uploads to extensions are streamed
		BodyLimit:         maxBody, // per-route limits apply to /api
		ReadBufferSize:    16 << 10,
		// fasthttp would otherwise read and parse every multipart body (up to maxBody,
		// into temp files) before the first middleware, authentication included, runs.
		DisablePreParseMultipartForm: true,
		// A client must complete the TLS handshake and the request head within
		// ReadTimeout (the proxy turns it into the longest pause of an upload), and
		// send its next request within IdleTimeout. Responses have no deadline
		// (WriteTimeout 0): event streams and downloads may run for hours, and
		// hijacked WebSockets have their deadlines cleared by fasthttp.
		ReadTimeout: d.Timeouts.Read,
		IdleTimeout: d.Timeouts.Idle,
	})
	app.Use(recover.New())
	app.Use(helmet.New(helmet.Config{XFrameOptions: "DENY", ReferrerPolicy: "no-referrer"}))
	app.Use(sh.cspHeader)
	app.Use(s.requestID)
	app.Use("/api", noStore, limitBody(apiBodyLimit))

	byIP, byUser := s.loginLimiters()
	app.Get("/api/health", s.health)
	app.Post("/api/auth/login", loginGuard, byIP, byUser, settleErrors, s.login)
	app.Post("/api/auth/logout", s.requireSession, s.csrf, s.logout)
	app.Get("/api/session", s.requireSession, s.getSession)
	app.Post("/api/session/admin", s.requireSession, s.csrf, s.adminLimiter(), settleErrors, s.adminOn)
	app.Delete("/api/session/admin", s.requireSession, s.csrf, s.adminOff)
	app.Get("/api/extensions", s.requireSession, s.listExtensions)
	app.Delete("/api/extensions/:id", s.requireSession, s.csrf, s.requireAdminMode, s.removeExtension)
	app.Post("/api/extensions/resolve", s.requireSession, s.csrf, s.requireAdminMode, s.resolveExtension)
	app.Post("/api/extensions/install", s.requireSession, s.csrf, s.requireAdminMode, s.installExtension)
	app.Get("/api/extensions/updates", s.requireSession, s.requireAdminMode, s.extensionUpdates)
	app.Post("/api/extensions/:id/update", s.requireSession, s.csrf, s.requireAdminMode, s.updateExtension)
	app.All("/api/*", func(c fiber.Ctx) error { return fiber.ErrNotFound })
	app.All("/ext/:id/api/*", s.requireSession, s.csrf, s.extAPI)
	app.All("/ext/:id/ui/*", s.requireSession, s.extUI)
	app.Get("/*", sh.handler)
	return app, nil
}

// noStore keeps API answers (sessions, CSRF tokens, system data) out of every
// cache, errors included: the header is set before anything can fail.
func noStore(c fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "no-store")
	return c.Next()
}

func (s *Server) health(c fiber.Ctx) error {
	return c.JSON(fiber.Map{"status": "ok", "version": s.d.Version})
}

func (s *Server) requestID(c fiber.Ctx) error {
	id := c.Get(sdk.HeaderRequestID)
	if id == "" || len(id) > 64 {
		b := make([]byte, 12)
		_, _ = rand.Read(b)
		id = hex.EncodeToString(b)
	}
	c.Locals(localReqID, id)
	c.Set(sdk.HeaderRequestID, id)
	return c.Next()
}

func requestIDFrom(c fiber.Ctx) string {
	id, _ := c.Locals(localReqID).(string)
	return id
}
