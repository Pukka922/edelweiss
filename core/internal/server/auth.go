package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/limiter"

	"github.com/Pukka922/edelweiss/core/internal/auth"
	"github.com/Pukka922/edelweiss/core/internal/proxy"
	"github.com/Pukka922/edelweiss/core/internal/session"
	"github.com/Pukka922/edelweiss/sdk"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type passwordRequest struct {
	Password string `json:"password"`
}

type userView struct {
	Name   string   `json:"name"`
	UID    int      `json:"uid"`
	Groups []string `json:"groups"`
}

type sessionView struct {
	User      userView `json:"user"`
	Admin     bool     `json:"admin"`
	CanAdmin  bool     `json:"canAdmin"`
	CSRFToken string   `json:"csrfToken"`
	Hostname  string   `json:"hostname"`
	Version   string   `json:"version"`
}

func (s *Server) view(sess session.Session) sessionView {
	groups := sess.User.Groups
	if groups == nil {
		groups = []string{}
	}
	return sessionView{
		User:  userView{Name: sess.User.Name, UID: sess.User.UID, Groups: groups},
		Admin: sess.Admin, CanAdmin: s.d.Auth.CanAdmin(sess.User), CSRFToken: sess.CSRFToken,
		Hostname: s.d.Hostname(), Version: s.d.Version,
	}
}

// isEncoded reports whether the Content-Encoding header lines name a content
// coding other than identity.
func isEncoded(lines [][]byte) bool {
	for _, line := range lines {
		for _, coding := range strings.Split(string(line), ",") {
			if coding = strings.ToLower(strings.TrimSpace(coding)); coding != "" && coding != "identity" {
				return true
			}
		}
	}
	return false
}

// limitBody caps request bodies (also streamed ones) for JSON API routes.
// Content codings are refused: Fiber's c.Body() would inflate an encoded body
// up to app.BodyLimit (gigabytes) although the wire size is within the limit.
func limitBody(max int) fiber.Handler {
	// A rejected body is left unread: close the connection after the response
	// so the leftover bytes are never parsed as the next request.
	reject := func(c fiber.Ctx, err *sdk.APIError) error {
		c.Response().SetConnectionClose()
		return err
	}
	return func(c fiber.Ctx) error {
		req := c.Request()
		if isEncoded(req.Header.PeekAll(fiber.HeaderContentEncoding)) {
			return reject(c, sdk.NewError(fiber.StatusUnsupportedMediaType, "unsupported_media_type", "encoded request bodies are not supported"))
		}
		if req.Header.ContentLength() > max {
			return reject(c, sdk.NewError(fiber.StatusRequestEntityTooLarge, "payload_too_large", "request body too large"))
		}
		if req.IsBodyStream() {
			data, err := io.ReadAll(io.LimitReader(req.BodyStream(), int64(max)+1))
			if err != nil {
				return reject(c, sdk.NewError(fiber.StatusBadRequest, "bad_request", "cannot read request body"))
			}
			if len(data) > max {
				return reject(c, sdk.NewError(fiber.StatusRequestEntityTooLarge, "payload_too_large", "request body too large"))
			}
			req.SetBody(data)
		}
		return c.Next()
	}
}

// decodeJSON parses the raw request body: c.Body() would inflate encoded ones.
func decodeJSON(c fiber.Ctx, v any) error {
	if err := json.Unmarshal(c.BodyRaw(), v); err != nil {
		return sdk.NewError(fiber.StatusBadRequest, "invalid_json", "request body must be valid JSON")
	}
	return nil
}

// settleErrors renders an error returned by the next handler right away.
// Fiber runs the ErrorHandler only after the whole middleware chain has
// returned, so a limiter in front of the handler would still see the default
// 200 for a returned *sdk.APIError and, with SkipSuccessfulRequests, treat a
// failed login as a success.
func settleErrors(c fiber.Ctx) error {
	if err := c.Next(); err != nil {
		return sdk.ErrorHandler(c, err)
	}
	return nil
}

// maxNameLen bounds the part of a login name that is kept: the limiter key lives
// in memory for the whole window and log lines are long-lived, while the name is
// chosen by an unauthenticated client.
const maxNameLen = 128

// clipName shortens a client-supplied login name to maxNameLen bytes, cutting at
// a character boundary.
func clipName(name string) string {
	if len(name) <= maxNameLen {
		return name
	}
	return strings.ToValidUTF8(name[:maxNameLen], "")
}

func tooManyAttempts(c fiber.Ctx) error {
	return sdk.WriteError(c, fiber.StatusTooManyRequests, "too_many_requests", "too many failed attempts, try again later")
}

// failureLimiter allows 10 failed requests per key and 5 minutes; successful
// ones are not counted. It relies on settleErrors behind it to see the status.
func failureLimiter(key func(fiber.Ctx) string) fiber.Handler {
	return limiter.New(limiter.Config{
		Max: 10, Expiration: 5 * time.Minute, SkipSuccessfulRequests: true,
		LimitReached: tooManyAttempts, KeyGenerator: key,
	})
}

func (s *Server) loginLimiters() (fiber.Handler, fiber.Handler) {
	byIP := failureLimiter(func(c fiber.Ctx) string { return "ip:" + c.IP() })
	byUser := failureLimiter(func(c fiber.Ctx) string {
		var r loginRequest
		_ = json.Unmarshal(c.BodyRaw(), &r)
		return "user:" + clipName(r.Username)
	})
	return byIP, byUser
}

// adminLimiter limits failed admin-mode password checks per user: they verify
// the same password as the login and must not be a way around its limiter.
func (s *Server) adminLimiter() fiber.Handler {
	return failureLimiter(func(c fiber.Ctx) string { return "admin:" + sessionFrom(c).User.Name })
}

func (s *Server) setSessionCookie(c fiber.Ctx, id string) {
	c.Cookie(&fiber.Cookie{
		Name: cookieSession, Value: id, Path: "/", HTTPOnly: true, SessionOnly: true,
		Secure: s.d.Config.TLS.Enabled, SameSite: fiber.CookieSameSiteStrictMode,
	})
}

func (s *Server) clearSessionCookie(c fiber.Ctx) {
	c.Cookie(&fiber.Cookie{
		Name: cookieSession, Value: "", Path: "/", HTTPOnly: true, MaxAge: -1, Expires: time.Unix(0, 0),
		Secure: s.d.Config.TLS.Enabled, SameSite: fiber.CookieSameSiteStrictMode,
	})
}

func (s *Server) requireSession(c fiber.Ctx) error {
	sess, ok := s.d.Sessions.Get(c.Cookies(cookieSession))
	if !ok {
		return sdk.NewError(fiber.StatusUnauthorized, "unauthenticated", "login required")
	}
	c.Locals(localSession, sess)
	return c.Next()
}

func sessionFrom(c fiber.Ctx) session.Session {
	sess, _ := c.Locals(localSession).(session.Session)
	return sess
}

// sameOrigin reports whether the request carries no Origin header or one that
// names the host and scheme the request was sent to.
func sameOrigin(c fiber.Ctx) bool {
	origin := c.Get(fiber.HeaderOrigin)
	return origin == "" || proxy.SameOrigin(origin, string(c.Request().Host()), c.RequestCtx().IsTLS())
}

var errCrossOrigin = sdk.NewError(fiber.StatusForbidden, "origin_mismatch", "cross-origin request rejected")

// csrf enforces the synchronizer token and same-origin for unsafe methods.
func (s *Server) csrf(c fiber.Ctx) error {
	switch c.Method() {
	case fiber.MethodGet, fiber.MethodHead, fiber.MethodOptions:
		return c.Next()
	}
	sess := sessionFrom(c)
	if subtle.ConstantTimeCompare([]byte(c.Get(headerCSRF)), []byte(sess.CSRFToken)) != 1 {
		return sdk.NewError(fiber.StatusForbidden, "csrf_invalid", "missing or invalid CSRF token")
	}
	if !sameOrigin(c) {
		return errCrossOrigin
	}
	return c.Next()
}

// loginGuard stands in for the CSRF token the login cannot have yet: only a
// JSON body (a cross-site form cannot send one without a preflight) from the
// same origin may create a session.
func loginGuard(c fiber.Ctx) error {
	if !sameOrigin(c) {
		return errCrossOrigin
	}
	if mediaType, _, err := mime.ParseMediaType(c.Get(fiber.HeaderContentType)); err != nil || mediaType != fiber.MIMEApplicationJSON {
		return sdk.NewError(fiber.StatusUnsupportedMediaType, "unsupported_media_type", "request body must be application/json")
	}
	return c.Next()
}

func (s *Server) requireAdminMode(c fiber.Ctx) error {
	if !sessionFrom(c).Admin {
		return sdk.NewError(fiber.StatusForbidden, "admin_required", "administrative access required")
	}
	return c.Next()
}

func (s *Server) login(c fiber.Ctx) error {
	var req loginRequest
	if err := decodeJSON(c, &req); err != nil {
		return err
	}
	u, err := s.d.Auth.Login(req.Username, req.Password)
	if errors.Is(err, auth.ErrInvalidCredentials) {
		s.d.Logger.Warn("login failed", "user", clipName(req.Username), "ip", c.IP())
		return sdk.NewError(fiber.StatusUnauthorized, "invalid_credentials", "invalid username or password")
	}
	if err != nil {
		s.d.Logger.Error("login error", "user", clipName(req.Username), "err", err)
		return sdk.NewError(fiber.StatusInternalServerError, "internal_error", "login failed")
	}
	// A browser that logs in again (as anyone) no longer needs its previous
	// session: drop it and stop what only it kept running.
	previous, replaced := s.d.Sessions.Get(c.Cookies(cookieSession))
	sess := s.d.Sessions.Create(u)
	if replaced {
		s.d.Sessions.Delete(previous.ID)
		s.stopUnused(previous.User.Name)
	}
	s.setSessionCookie(c, sess.ID)
	s.d.Logger.Info("login", "user", u.Name, "ip", c.IP())
	return c.JSON(s.view(sess))
}

func (s *Server) logout(c fiber.Ctx) error {
	sess := sessionFrom(c)
	s.d.Sessions.Delete(sess.ID)
	s.clearSessionCookie(c)
	s.stopUnused(sess.User.Name)
	s.d.Logger.Info("logout", "user", sess.User.Name)
	return c.SendStatus(fiber.StatusNoContent)
}

// stopUnused stops extension processes the user no longer needs.
func (s *Server) stopUnused(user string) {
	if !s.d.Sessions.HasSession(user, false) {
		go s.d.Pool.StopUser(user, false)
		return
	}
	if !s.d.Sessions.HasSession(user, true) {
		go s.d.Pool.StopUser(user, true)
	}
}

func (s *Server) getSession(c fiber.Ctx) error {
	return c.JSON(s.view(sessionFrom(c)))
}

func (s *Server) adminOn(c fiber.Ctx) error {
	sess := sessionFrom(c)
	// Decide on the account as it is now, not as it was at login: the user may
	// have left (or joined) the admin group since. The session keeps the fresh
	// account, so canAdmin and the process identity follow as well.
	fresh, err := s.d.Auth.Refresh(sess.User.Name)
	if err != nil {
		s.d.Logger.Error("admin mode account lookup error", "user", sess.User.Name, "err", err)
		return sdk.NewError(fiber.StatusInternalServerError, "internal_error", "verification failed")
	}
	sess, ok := s.d.Sessions.UpdateUser(sess.ID, fresh)
	if !ok {
		return sdk.NewError(fiber.StatusUnauthorized, "unauthenticated", "login required")
	}
	if !s.d.Auth.CanAdmin(sess.User) {
		return sdk.NewError(fiber.StatusForbidden, "admin_not_permitted", "your account may not use administrative access")
	}
	var req passwordRequest
	if err := decodeJSON(c, &req); err != nil {
		return err
	}
	if err := s.d.Auth.Reauthenticate(sess.User, req.Password); err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			s.d.Logger.Warn("admin mode denied", "user", sess.User.Name, "ip", c.IP())
			return sdk.NewError(fiber.StatusUnauthorized, "invalid_credentials", "invalid password")
		}
		s.d.Logger.Error("admin mode verification error", "user", sess.User.Name, "err", err)
		return sdk.NewError(fiber.StatusInternalServerError, "internal_error", "verification failed")
	}
	updated, ok := s.d.Sessions.SetAdmin(sess.ID, true)
	if !ok {
		return sdk.NewError(fiber.StatusUnauthorized, "unauthenticated", "login required")
	}
	s.setSessionCookie(c, updated.ID)
	s.d.Logger.Info("admin mode enabled", "user", sess.User.Name, "ip", c.IP())
	return c.JSON(s.view(updated))
}

func (s *Server) adminOff(c fiber.Ctx) error {
	sess := sessionFrom(c)
	updated, ok := s.d.Sessions.SetAdmin(sess.ID, false)
	if !ok {
		return sdk.NewError(fiber.StatusUnauthorized, "unauthenticated", "login required")
	}
	s.setSessionCookie(c, updated.ID)
	if !s.d.Sessions.HasSession(sess.User.Name, true) {
		go s.d.Pool.StopUser(sess.User.Name, true)
	}
	s.d.Logger.Info("admin mode disabled", "user", sess.User.Name)
	return c.JSON(s.view(updated))
}
