package server

import (
	"crypto/sha256"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gofiber/fiber/v3"

	"github.com/Pukka922/edelweiss/sdk"
)

func indexWithMap(imports string) string {
	return `<!doctype html><html><head><script type="importmap">` + imports +
		`</script><script type="module" crossorigin src="/assets/app.js"></script></head><body></body></html>`
}

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

func TestCSPFor(t *testing.T) {
	csp, err := cspFor([]byte(indexWithMap(`{"imports":{"solid-js":"/vendor/solid.js"}}`)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(csp, hashOf(`{"imports":{"solid-js":"/vendor/solid.js"}}`)) || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("csp %q", csp)
	}
	if _, err := cspFor([]byte(placeholderIndex)); err != nil {
		t.Fatalf("placeholder: %v", err)
	}
	bad := map[string]string{
		"two import maps":            `<script type="importmap">{}</script><script type="importmap">{}</script>`,
		"unhashable map":             `<script type="importmap" nonce="x">{}</script>`,
		"extra inline code":          indexWithMap(`{}`) + `<script>document.title = "x"</script>`,
		"data-src is not a src":      `<script data-src="x">alert(1)</script>`,
		"src in a quoted value":      `<script title="a src=b">alert(1)</script>`,
		"src in an unquoted value":   `<script title=a/src=b>alert(1)</script>`,
		"src in an unclosed quote":   `<script title="a src=b>alert(1)</script>`,
		"inline code after real src": `<script src="/a.js"></script><script data-src='x'>alert(1)</script>`,
	}
	for name, index := range bad {
		if _, err := cspFor([]byte(index)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	good := map[string]string{
		"module script":            `<script type="module" src="/assets/x.js"></script>`,
		"src after a quoted value": `<script title="a src=b" SRC='/assets/x.js'></script>`,
		"src without whitespace":   `<script type="module"src="/assets/x.js"></script>`,
		"src on its own line":      "<script type=\"module\"\n  src=\"/assets/x.js\"></script>",
	}
	for name, index := range good {
		if _, err := cspFor([]byte(index)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Browsers hash the script text after turning CRLF and CR into LF.
func TestCSPForNormalisesLineEndings(t *testing.T) {
	lf := "{\n\"imports\": {\n\"a\": \"/a.js\"\n}\n}"
	for name, imports := range map[string]string{
		"lf":   lf,
		"crlf": strings.ReplaceAll(lf, "\n", "\r\n"),
		"cr":   strings.ReplaceAll(lf, "\n", "\r"),
	} {
		csp, err := cspFor([]byte(indexWithMap(imports)))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(csp, hashOf(lf)) {
			t.Errorf("%s: csp %q lacks the hash of the LF text", name, csp)
		}
	}
}

func shellApp(t *testing.T, sh *shell) *fiber.App {
	t.Helper()
	app := fiber.New(fiber.Config{ErrorHandler: sdk.ErrorHandler})
	app.Use(sh.cspHeader)
	app.Get("/*", sh.handler)
	return app
}

func shellDo(t *testing.T, app *fiber.App, path string) *http.Response {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest("GET", path, nil))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func shellGet(t *testing.T, app *fiber.App, path string) (string, string, string) {
	t.Helper()
	resp := shellDo(t, app, path)
	body, _ := io.ReadAll(resp.Body)
	return string(body), resp.Header.Get("Content-Security-Policy"), resp.Header.Get("Cache-Control")
}

func writeShell(t *testing.T, dir, imports string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(indexWithMap(imports)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestShellDevModeReloadsIndex(t *testing.T) {
	dir := t.TempDir()
	writeShell(t, dir, `{"imports":{"a":"/a.js"}}`)
	sh, err := newShell(os.DirFS(dir), true, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	app := shellApp(t, sh)
	body, csp, _ := shellGet(t, app, "/")
	if !strings.Contains(body, `"a":"/a.js"`) || !strings.Contains(csp, hashOf(`{"imports":{"a":"/a.js"}}`)) {
		t.Fatalf("first: %q / %q", body, csp)
	}
	writeShell(t, dir, `{"imports":{"b":"/b.js"}}`)
	body, csp, _ = shellGet(t, app, "/x/system")
	if !strings.Contains(body, `"b":"/b.js"`) || !strings.Contains(csp, hashOf(`{"imports":{"b":"/b.js"}}`)) {
		t.Fatalf("after rebuild: %q / %q", body, csp)
	}
	if _, _, cc := shellGet(t, app, "/assets/app.js"); cc != "no-cache" {
		t.Fatalf("dev assets cache-control %q", cc)
	}
}

// A watch build can leave an index.html behind that the CSP cannot cover: the
// request fails with shell_invalid, the last valid index and CSP stay in place,
// and the next valid build is picked up again.
func TestShellDevModeInvalidIndexKeepsLastValidCSP(t *testing.T) {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil))) // the error handler logs the 500
	t.Cleanup(func() { slog.SetDefault(prev) })

	dir := t.TempDir()
	writeShell(t, dir, `{"imports":{"a":"/a.js"}}`)
	sh, err := newShell(os.DirFS(dir), true, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	app := shellApp(t, sh)
	if _, csp, _ := shellGet(t, app, "/"); !strings.Contains(csp, hashOf(`{"imports":{"a":"/a.js"}}`)) {
		t.Fatalf("first: csp %q", csp)
	}

	broken := indexWithMap(`{"imports":{"b":"/b.js"}}`) + `<script>document.title = "x"</script>`
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	resp := shellDo(t, app, "/")
	if resp.StatusCode != 500 || errorCode(t, resp) != "shell_invalid" {
		t.Fatalf("broken index: status %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Security-Policy"); !strings.Contains(got, hashOf(`{"imports":{"a":"/a.js"}}`)) || strings.Contains(got, hashOf(`{"imports":{"b":"/b.js"}}`)) {
		t.Fatalf("broken index: csp %q is not the last valid one", got)
	}
	if body, csp, _ := shellGet(t, app, "/assets/app.js"); body != "1" || !strings.Contains(csp, hashOf(`{"imports":{"a":"/a.js"}}`)) {
		t.Fatalf("asset while broken: %q / %q", body, csp)
	}

	writeShell(t, dir, `{"imports":{"c":"/c.js"}}`)
	body, csp, _ := shellGet(t, app, "/")
	if !strings.Contains(body, `"c":"/c.js"`) || !strings.Contains(csp, hashOf(`{"imports":{"c":"/c.js"}}`)) {
		t.Fatalf("after fix: %q / %q", body, csp)
	}
}

func TestShellProductionKeepsIndexAndCachesAssets(t *testing.T) {
	dir := t.TempDir()
	writeShell(t, dir, `{"imports":{"a":"/a.js"}}`)
	sh, err := newShell(os.DirFS(dir), false, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	app := shellApp(t, sh)
	writeShell(t, dir, `{"imports":{"b":"/b.js"}}`)
	body, csp, _ := shellGet(t, app, "/")
	if !strings.Contains(body, `"a":"/a.js"`) || !strings.Contains(csp, hashOf(`{"imports":{"a":"/a.js"}}`)) {
		t.Fatalf("production index changed: %q", body)
	}
	if _, csp, cc := shellGet(t, app, "/assets/app.js"); !strings.Contains(cc, "immutable") || csp == "" {
		t.Fatalf("asset headers: cache-control %q csp %q", cc, csp)
	}
}

func TestNewRejectsInvalidShell(t *testing.T) {
	_, err := newShell(fstestFS(map[string]string{"index.html": `<script>alert(1)</script>`}), false, slog.Default())
	if err == nil {
		t.Fatal("expected error for inline script")
	}
}

func fstestFS(files map[string]string) fstest.MapFS {
	m := fstest.MapFS{}
	for name, content := range files {
		m[name] = &fstest.MapFile{Data: []byte(content)}
	}
	return m
}
