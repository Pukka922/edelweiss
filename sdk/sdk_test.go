package sdk

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/sse"
)

const testManifest = `{"id":"demo","version":"1.0.0","apiVersion":1,"name":{"en":"Demo"},
"menu":{"section":"tools","icon":"icon.svg"},"ui":{"entry":"index.js"}}`

func testUI() fstest.MapFS {
	return fstest.MapFS{
		"index.js": {Data: []byte("export default 1")},
		"icon.svg": {Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)},
	}
}

func TestNewPanicsOnInvalidManifest(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	New(Options{Manifest: []byte(`{"id":"X"}`)})
}

func TestManifestJSON(t *testing.T) {
	ext := New(Options{Manifest: []byte(testManifest), UI: testUI()})
	out, err := ext.ManifestJSON()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["sdkVersion"] != Version {
		t.Errorf("sdkVersion = %v", m["sdkVersion"])
	}
	if m["iconSvg"] != `<svg xmlns="http://www.w3.org/2000/svg"/>` {
		t.Errorf("iconSvg = %v", m["iconSvg"])
	}
}

func TestManifestJSONMissingIcon(t *testing.T) {
	ext := New(Options{Manifest: []byte(testManifest), UI: fstest.MapFS{"index.js": {Data: []byte("x")}}})
	if _, err := ext.ManifestJSON(); err == nil {
		t.Fatal("expected error for missing icon")
	}
}

func TestStatusBusy(t *testing.T) {
	ext := New(Options{Manifest: []byte(testManifest), UI: testUI()})
	get := func() bool {
		resp, err := ext.App.Test(httptest.NewRequest("GET", StatusPath, nil))
		if err != nil {
			t.Fatal(err)
		}
		var s struct{ Busy bool }
		_ = json.NewDecoder(resp.Body).Decode(&s)
		return s.Busy
	}
	if get() {
		t.Fatal("busy before Busy()")
	}
	done := Busy()
	if !get() {
		t.Fatal("not busy after Busy()")
	}
	done()
	done() // idempotent
	if get() {
		t.Fatal("busy after done()")
	}
}

// rawDo sends a request with target as the request line, verbatim (for paths
// httptest.NewRequest would clean or refuse).
func rawDo(t *testing.T, app *fiber.App, method, target string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, "/", nil)
	req.URL.Opaque = target // used verbatim as the request target
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func rawGet(t *testing.T, app *fiber.App, target string) (int, string) {
	t.Helper()
	return rawDo(t, app, "GET", target)
}

// The router resolves dot segments, also percent-encoded, before it matches a
// route, and keeps an encoded slash. A core that does not normalize the same way
// (edelweissd up to 0.2.0) forwards /ext/<id>/api/a%2Fb/%2e%2e/_edelweiss/status as
// /a%2Fb/%2e%2e/_edelweiss/status, which the router would match as StatusPath, and
// /_edelweiss/ui/a%2Fb/c%2Fd/%2e%2e/%2e%2e/%2e%2e/%2e%2e/hello, which it would match
// as the extension's own /hello. So a path reaches an internal route only if it
// literally names it, and an app route only if it names no internal route.
func TestInternalRoutesOnlyForTheirLiteralPath(t *testing.T) {
	ui := testUI()
	ui["a b.js"] = &fstest.MapFile{Data: []byte("space")}
	ui["é.js"] = &fstest.MapFile{Data: []byte("accent")}
	ui["a/b.js"] = &fstest.MapFile{Data: []byte("slash")}
	ui["sub/x.js"] = &fstest.MapFile{Data: []byte("sub")}
	for _, cfg := range []*fiber.Config{nil, {UnescapePath: true}} {
		ext := New(Options{Manifest: []byte(testManifest), UI: ui, Config: cfg})
		ext.App.Get("/hello", func(c fiber.Ctx) error { return c.SendString("hello from the app") })
		name := "default config"
		if cfg != nil {
			name = "UnescapePath"
		}
		for _, p := range []string{
			"/api/a%2Fb/%2e%2e/_edelweiss/status", "/a%2Fb/%2e%2e/_edelweiss/status", "/a%2Fb/%2e%2e/_edelweiss/ui/index.js",
			"/x/%2e%2e/_edelweiss/status", "/%2e%2e/_edelweiss/ui/index.js", "/./_edelweiss/status", "/x/../_edelweiss/status",
			"/%5Fedelweiss/status", "/_EDELWEISS/status", "/_edelweiss%2Fstatus",
			// from one internal route to the other
			"/_edelweiss/ui/a%2Fb/%2e%2e/%2e%2e/status", "/_edelweiss/ui/../status", "/_edelweiss/status/%2e%2e/ui/index.js",
			// from an internal route to a route of the app
			"/_edelweiss/status/%2e%2e/%2e%2e/hello", "/_edelweiss/ui/a%2Fb/c%2Fd/%2e%2e/%2e%2e/%2e%2e/%2e%2e/hello",
			"/_edelweiss/ui/..%2F..%2Fhello", "/_edelweiss/../hello",
		} {
			status, body := rawGet(t, ext.App, p)
			if status != 404 || strings.Contains(body, `"busy"`) || strings.Contains(body, "export default 1") ||
				strings.Contains(body, "hello from the app") {
				t.Errorf("%s: %s: status %d body %q, want 404 without reaching a route", name, p, status, body)
			}
		}
		for _, tc := range []struct{ method, path, want string }{
			{"GET", StatusPath, `{"busy":false}`}, {"GET", StatusPath + "/", `{"busy":false}`},
			{"GET", StatusPath + "?x=/../ui/index.js", `{"busy":false}`}, {"HEAD", StatusPath, ""},
			{"GET", UIPrefix + "/index.js", "export default 1"}, {"GET", UIPrefix + "/index.js?v=123", "export default 1"},
			{"GET", UIPrefix + "/a%20b.js", "space"}, {"GET", UIPrefix + "/%C3%A9.js", "accent"},
			{"GET", UIPrefix + "/a%2Fb.js", "slash"}, {"GET", UIPrefix + "/sub/x.js", "sub"},
			{"GET", UIPrefix + "/./index.js", "export default 1"}, {"GET", UIPrefix + "/x/../index.js", "export default 1"},
			{"GET", UIPrefix + "/x/%2e%2e/index.js", "export default 1"}, {"GET", UIPrefix + "//index.js", "export default 1"},
			{"GET", UIPrefix + "/%69ndex.js", "export default 1"}, {"HEAD", UIPrefix + "/index.js", ""},
			{"GET", "/hello", "hello from the app"}, {"GET", "/x/../hello", "hello from the app"},
		} {
			if status, body := rawDo(t, ext.App, tc.method, tc.path); status != 200 || body != tc.want {
				t.Errorf("%s: %s %s: status %d body %q, want 200 %q", name, tc.method, tc.path, status, body, tc.want)
			}
		}
	}
}

func TestSSE(t *testing.T) {
	ext := New(Options{Manifest: []byte(testManifest)})
	ext.App.Get("/events", SSE(func(c fiber.Ctx, s *sse.Stream) error {
		if err := s.Event(sse.Event{Name: "tick", Data: map[string]int{"n": 1}}); err != nil {
			return err
		}
		return s.Event(sse.Event{Name: "tick", Data: map[string]int{"n": 2}})
	}))
	resp, err := ext.App.Test(httptest.NewRequest("GET", "/events", nil))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("content-type %q", resp.Header.Get("Content-Type"))
	}
	if want := "event: tick\ndata: {\"n\":2}\n\n"; !strings.Contains(string(body), want) {
		t.Errorf("body %q missing %q", body, want)
	}
}
