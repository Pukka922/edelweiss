// Package webui embeds the built web shell (web/shell/dist, copied here by
// `make build`). Until the shell is built only .gitkeep is embedded and the
// server falls back to a placeholder page.
package webui

import (
	"embed"
	"io/fs"
	"os"
)

//go:embed all:dist
var dist embed.FS

// FS returns the shell files; devDir (if set) serves them from disk instead.
func FS(devDir string) (fs.FS, error) {
	if devDir != "" {
		return os.DirFS(devDir), nil
	}
	return fs.Sub(dist, "dist")
}
