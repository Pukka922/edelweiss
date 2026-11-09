package sdk

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/recover"

	"github.com/Pukka922/edelweiss/sdk/manifest"
)

// Options configure an extension.
type Options struct {
	// Manifest is the raw manifest.json (usually via go:embed).
	Manifest []byte
	// UI is the built frontend, rooted at the directory containing the entry
	// file (e.g. fs.Sub(embedded, "ui/dist")). Optional.
	UI fs.FS
	// Config optionally overrides the Fiber configuration. If its
	// ErrorHandler is nil, the edelweiss error handler is used.
	Config *fiber.Config
}

// Extension is an edelweiss extension: a Fiber app plus its manifest.
type Extension struct {
	App      *fiber.App
	Manifest *manifest.Manifest
	ui       fs.FS
}

// New creates an extension. It panics if the manifest is invalid, because that
// is a programming error in the extension.
func New(opts Options) *Extension {
	m, err := manifest.Parse(opts.Manifest)
	if err != nil {
		panic(fmt.Sprintf("edelweiss sdk: %v", err))
	}
	cfg := fiber.Config{}
	if opts.Config != nil {
		cfg = *opts.Config
	}
	if cfg.ErrorHandler == nil {
		cfg.ErrorHandler = ErrorHandler
	}
	if cfg.AppName == "" {
		cfg.AppName = m.ID
	}
	app := fiber.New(cfg)
	app.Use(recover.New())
	app.Use(confineInternalRoutes)
	app.Get(StatusPath, statusHandler)

	ui := opts.UI
	if dir := os.Getenv(EnvDevUIDir); dir != "" {
		ui = os.DirFS(dir)
	}
	if ui != nil {
		app.Get(UIPrefix+"/*", uiHandler(ui))
	}
	return &Extension{App: app, Manifest: m, ui: ui}
}

// confineInternalRoutes answers 404 for a request the router would match in another
// part of the app than its path, as sent, literally names: in an internal route of
// InternalPrefix other than that one, or, for a path that names one, in a route of
// the app outside InternalPrefix. The router resolves dot segments, also
// percent-encoded, and keeps an encoded slash, so /a%2Fb/%2e%2e/_edelweiss/status is
// matched as StatusPath, /_edelweiss/ui/a%2Fb/%2e%2e/%2e%2e/status as well, and
// /_edelweiss/status/%2e%2e/%2e%2e/hello as the app's /hello; edelweissd up to 0.2.0
// forwards such paths, since it checks them without that normalization.
func confineInternalRoutes(c fiber.Ctx) error {
	routed := internalRoute(strings.ToLower(c.Path())) // the router ignores case by default
	if routed != internalRoute(string(c.Request().URI().PathOriginal())) {
		return fiber.ErrNotFound
	}
	return c.Next()
}

// internalRoute returns the internal route p lies in, UIPrefix or InternalPrefix
// (StatusPath and the rest), or "" for a path outside InternalPrefix.
func internalRoute(p string) string {
	switch {
	case p == UIPrefix || strings.HasPrefix(p, UIPrefix+"/"):
		return UIPrefix
	case p == InternalPrefix || strings.HasPrefix(p, InternalPrefix+"/"):
		return InternalPrefix
	}
	return ""
}

// ManifestJSON returns the manifest as reported to edelweissd: with sdkVersion
// set and the menu icon inlined as SVG.
func (e *Extension) ManifestJSON() ([]byte, error) {
	m := *e.Manifest
	m.SDKVersion = Version
	if m.Menu.Icon != "" {
		if e.ui == nil {
			return nil, fmt.Errorf("manifest: menu.icon %q set but extension has no UI", m.Menu.Icon)
		}
		svg, err := fs.ReadFile(e.ui, m.Menu.Icon)
		if err != nil {
			return nil, fmt.Errorf("manifest: menu.icon: %w", err)
		}
		m.IconSVG = string(svg)
	}
	return json.Marshal(&m)
}
