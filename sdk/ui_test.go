package sdk

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestUIServing(t *testing.T) {
	ext := New(Options{Manifest: []byte(testManifest), UI: testUI()})

	resp, err := ext.App.Test(httptest.NewRequest("GET", UIPrefix+"/index.js", nil))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "export default 1" {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/javascript; charset=utf-8" {
		t.Errorf("content-type %q", ct)
	}
	if resp.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("cache-control %q", resp.Header.Get("Cache-Control"))
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}

	req := httptest.NewRequest("GET", UIPrefix+"/index.js", nil)
	req.Header.Set("If-None-Match", etag)
	resp, _ = ext.App.Test(req)
	if resp.StatusCode != 304 {
		t.Fatalf("conditional request: status %d", resp.StatusCode)
	}

	for _, p := range []string{UIPrefix + "/missing.js", UIPrefix + "/../sdk.go", UIPrefix + "/"} {
		resp, _ = ext.App.Test(httptest.NewRequest("GET", p, nil))
		if resp.StatusCode != 404 {
			t.Errorf("%s: status %d, want 404", p, resp.StatusCode)
		}
	}
}

func TestUIDevDirOverride(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.js"), []byte("dev build"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvDevUIDir, dir)
	ext := New(Options{Manifest: []byte(testManifest), UI: testUI()})
	resp, _ := ext.App.Test(httptest.NewRequest("GET", UIPrefix+"/index.js", nil))
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "dev build" {
		t.Fatalf("body %q", body)
	}
}

func TestNoUIRoute(t *testing.T) {
	ext := New(Options{Manifest: []byte(`{"id":"noui","version":"1.0.0","apiVersion":1,"name":{"en":"x"},"menu":{"section":"tools"}}`)})
	resp, _ := ext.App.Test(httptest.NewRequest("GET", UIPrefix+"/index.js", nil))
	if resp.StatusCode != 404 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestUIPercentEncodedNames(t *testing.T) {
	fsys := testUI()
	fsys["my file.js"] = &fstest.MapFile{Data: []byte("spaced")}
	fsys["ü.js"] = &fstest.MapFile{Data: []byte("umlaut")}
	ext := New(Options{Manifest: []byte(testManifest), UI: fsys})

	for _, tc := range []struct{ url, want string }{
		{UIPrefix + "/my%20file.js", "spaced"},
		{UIPrefix + "/%C3%BC.js", "umlaut"},
		{UIPrefix + "/%69ndex.js", "export default 1"},
	} {
		resp, err := ext.App.Test(httptest.NewRequest("GET", tc.url, nil))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 || string(body) != tc.want {
			t.Errorf("%s: status %d body %q, want 200 %q", tc.url, resp.StatusCode, body, tc.want)
		}
	}
}

func TestUIRejectsEncodedTraversal(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "secret.txt"), []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "dist")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "index.js"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	ext := New(Options{Manifest: []byte(testManifest), UI: os.DirFS(sub)})

	for _, p := range []string{
		UIPrefix + "/..%2fsdk.go",
		UIPrefix + "/%2e%2e/x",
		UIPrefix + "/..%2fsecret.txt",
		UIPrefix + "/%2e%2e%2fsecret.txt",
		UIPrefix + "/..%5csecret.txt",
		UIPrefix + "/%00",
		UIPrefix + "/%zz",
		UIPrefix + "/bad%",
	} {
		// Set RequestURI directly: httptest.NewRequest rejects invalid escapes
		// such as "%zz", but the server must still answer them with 404.
		req := httptest.NewRequest("GET", UIPrefix+"/x", nil)
		req.RequestURI = p
		resp, err := ext.App.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 404 {
			t.Errorf("%s: status %d, want 404", p, resp.StatusCode)
		}
		if strings.Contains(string(body), "top secret") {
			t.Errorf("%s: leaked secret", p)
		}
	}
}

func TestUIHead(t *testing.T) {
	ext := New(Options{Manifest: []byte(testManifest), UI: testUI()})
	resp, err := ext.App.Test(httptest.NewRequest("HEAD", UIPrefix+"/index.js", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if resp.Header.Get("ETag") == "" {
		t.Error("missing ETag")
	}
}

func TestUINotFoundUsesErrorEnvelope(t *testing.T) {
	ext := New(Options{Manifest: []byte(testManifest), UI: testUI()})
	resp, err := ext.App.Test(httptest.NewRequest("GET", UIPrefix+"/missing.js", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 404 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	if env.Error.Code != "not_found" {
		t.Errorf("error code %q, want not_found", env.Error.Code)
	}
}
