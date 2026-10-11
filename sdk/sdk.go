package sdk

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"

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
