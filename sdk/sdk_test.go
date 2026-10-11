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
