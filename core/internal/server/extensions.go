package server

import (
	"encoding/base64"
	"errors"
	"io/fs"
	"net/url"
	"path"
	"strings"

	"github.com/gofiber/fiber/v3"

	"github.com/Pukka922/edelweiss/core/internal/extmgr"
	"github.com/Pukka922/edelweiss/core/internal/procman"
	"github.com/Pukka922/edelweiss/core/internal/proxy"
	"github.com/Pukka922/edelweiss/core/internal/session"
	"github.com/Pukka922/edelweiss/sdk"
	"github.com/Pukka922/edelweiss/sdk/manifest"
)

type uiView struct {
	Entry   string   `json:"entry"`
	Styles  []string `json:"styles"`
	Locales string   `json:"locales,omitempty"`
}

type requiresView struct {
	APK []string `json:"apk"`
}

type extensionView struct {
	ID              string             `json:"id"`
	Version         string             `json:"version"`
	Name            manifest.Localized `json:"name"`
	Description     manifest.Localized `json:"description,omitempty"`
	Menu            manifest.Menu      `json:"menu"`
	Icon            string             `json:"icon,omitempty"`
	UI              *uiView            `json:"ui,omitempty"`
	RequiresAdmin   bool               `json:"requiresAdmin"`
	Requires        requiresView       `json:"requires"`
	MissingPackages []string           `json:"missingPackages"`
	Source          extmgr.Source      `json:"source"`
	Dev             bool               `json:"dev"`
}

func viewOf(e *extmgr.Extension, missing []string) extensionView {
	m := e.Manifest
	apk := m.RequiredAPK()
	if apk == nil {
		apk = []string{}
	}
	if missing == nil {
		missing = []string{}
	}
	v := extensionView{ID: m.ID, Version: m.Version, Name: m.Name, Description: m.Description, Menu: m.Menu,
		RequiresAdmin: m.RequiresAdmin, Requires: requiresView{APK: apk}, MissingPackages: missing, Source: e.Source,
		Dev: e.Dev}
	v.Menu.Icon = ""
	if m.IconSVG != "" {
		v.Icon = "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(m.IconSVG))
	}
	if m.UI != nil {
		base := "/ext/" + m.ID + "/ui/"
		ui := &uiView{Entry: base + m.UI.Entry, Styles: []string{}}
		for _, st := range m.UI.Styles {
			ui.Styles = append(ui.Styles, base+st)
		}
		if m.UI.Locales != "" {
			ui.Locales = base + m.UI.Locales
		}
		v.UI = ui
	}
	return v
}

// extView is the API entry of e, with the packages it misses (view is taken by the session answer).
func (s *Server) extView(e *extmgr.Extension) extensionView {
	return viewOf(e, s.d.Registry.MissingPackages(e))
}

func (s *Server) listExtensions(c fiber.Ctx) error {
	list := s.d.Registry.List()
	out := make([]extensionView, 0, len(list))
	for _, e := range list {
		out = append(out, s.extView(e))
	}
	return c.JSON(fiber.Map{"extensions": out})
}

func (s *Server) removeExtension(c fiber.Ctx) error {
	id := c.Params("id")
	err := s.d.Registry.Remove(id)
	if errors.Is(err, extmgr.ErrNotFound) {
		return sdk.NewError(fiber.StatusNotFound, "extension_not_found", "extension not found")
	}
	var dataErr *extmgr.DataNotRemovedError
	if err != nil && !errors.As(err, &dataErr) {
		s.d.Logger.Error("remove extension failed", "id", id, "err", err)
		return sdk.NewError(fiber.StatusInternalServerError, "internal_error", "could not remove extension")
	}
	s.d.Logger.Info("extension removed", "id", id, "user", sessionFrom(c).User.Name)
	if dataErr != nil {
		// The extension is gone, so nothing can be retried: tell the user what is left to delete by hand.
		s.d.Logger.Error("extension data directory not deleted", "id", id, "dir", dataErr.Dir, "err", dataErr.Err)
		return sdk.NewError(fiber.StatusInternalServerError, "data_not_removed",
			"the extension was removed, but its data directory "+dataErr.Dir+" could not be deleted")
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func (s *Server) lookupExtension(c fiber.Ctx) (*extmgr.Extension, session.Session, error) {
	sess := sessionFrom(c)
	ext, ok := s.d.Registry.Get(c.Params("id"))
	if !ok {
		return nil, sess, sdk.NewError(fiber.StatusNotFound, "extension_not_found", "extension not found")
	}
	if ext.Manifest.RequiresAdmin && !sess.Admin {
		return nil, sess, sdk.NewError(fiber.StatusForbidden, "admin_required", "administrative access required")
	}
	return ext, sess, nil
}

// reservedPath reports whether p lies in the extension's reserved
// sdk.InternalPrefix namespace (status, ui, ...), ignoring case.
func reservedPath(p string) bool {
	lp := strings.ToLower(p)
	return lp == sdk.InternalPrefix || strings.HasPrefix(lp, sdk.InternalPrefix+"/")
}

// addressesReserved reports whether the wildcard of an extension API request
// (percent-encoded, as Fiber hands it out) can address the reserved namespace.
// The extension decodes the path itself, so "%5Fedelweiss" or "x/%2e%2e/_edelweiss"
// must be caught as well; a path that cannot be decoded is refused outright
// because a lenient decoder on the other side could read it differently.
func addressesReserved(raw string) bool {
	if reservedPath(path.Clean("/" + raw)) {
		return true
	}
	dec, err := url.PathUnescape(raw)
	return err != nil || reservedPath(path.Clean("/"+dec))
}

func (s *Server) extAPI(c fiber.Ctx) error {
	ext, sess, err := s.lookupExtension(c)
	if err != nil {
		return err
	}
	raw := c.Params("*")
	if addressesReserved(raw) {
		return fiber.ErrNotFound
	}
	p := path.Clean("/" + raw)
	if q := string(c.Request().URI().QueryString()); q != "" {
		p += "?" + q
	}
	return s.forward(c, ext, sess, p)
}

// plainFile reports whether the wildcard of an extension UI request (percent-
// encoded, as Fiber hands it out) names a plain relative file path once the
// extension has decoded it: no escape from the UI directory (also not via an
// encoded slash or dot), nothing absolute, no NUL, nothing undecodable.
func plainFile(raw string) bool {
	dec, err := url.PathUnescape(raw)
	if err != nil || strings.ContainsRune(dec, 0) {
		return false
	}
	name := path.Clean(dec)
	return name != "." && fs.ValidPath(name)
}

func (s *Server) extUI(c fiber.Ctx) error {
	if c.Method() != fiber.MethodGet && c.Method() != fiber.MethodHead {
		return fiber.ErrMethodNotAllowed
	}
	ext, sess, err := s.lookupExtension(c)
	if err != nil {
		return err
	}
	raw := c.Params("*")
	if !plainFile(raw) {
		return fiber.ErrNotFound
	}
	return s.forward(c, ext, sess, sdk.UIPrefix+path.Clean("/"+raw))
}

func (s *Server) forward(c fiber.Ctx, ext *extmgr.Extension, sess session.Session, target string) error {
	ident, err := s.identity(sess)
	if err != nil {
		s.d.Logger.Error("resolve process identity", "user", sess.User.Name, "err", err)
		return sdk.NewError(fiber.StatusServiceUnavailable, "backend_unavailable", "extension could not be started")
	}
	spec := procman.Spec{
		Key:    procman.Key{User: sess.User.Name, Admin: sess.Admin, ExtID: ext.Manifest.ID},
		Binary: ext.Binary, Identity: ident, DevUIDir: ext.UIDir,
	}
	proc, release, err := s.d.Pool.Acquire(c.Context(), spec)
	if errors.Is(err, procman.ErrCrashLoop) {
		return sdk.NewError(fiber.StatusServiceUnavailable, "backend_crashloop", "extension is crashing repeatedly, try again later")
	}
	if err != nil {
		s.d.Logger.Error("start extension failed", "ext", ext.Manifest.ID, "user", sess.User.Name, "err", err)
		return sdk.NewError(fiber.StatusServiceUnavailable, "backend_unavailable", "extension could not be started")
	}
	id := proxy.Identity{User: sess.User.Name, UID: sess.User.UID, Admin: sess.Admin, Lang: lang(c), RequestID: requestIDFrom(c)}
	if proxy.IsUpgrade(c) {
		return proxy.ForwardWebSocket(c, proc, target, id, release)
	}
	return proxy.Forward(c, proc, target, id, release)
}

// identity is the OS account for the process: the user, or root in admin mode.
func (s *Server) identity(sess session.Session) (procman.Identity, error) {
	u := sess.User
	if sess.Admin {
		root, err := s.d.Auth.Root()
		if err != nil {
			return procman.Identity{}, err
		}
		u = root
	}
	return procman.Identity{Name: u.Name, UID: u.UID, GID: u.GID, GroupIDs: u.GroupIDs, Home: u.Home}, nil
}

func lang(c fiber.Ctx) string {
	if l := c.Cookies(cookieLang); l == "de" || l == "en" {
		return l
	}
	if strings.HasPrefix(strings.ToLower(c.Get(fiber.HeaderAcceptLanguage)), "de") {
		return "de"
	}
	return "en"
}
