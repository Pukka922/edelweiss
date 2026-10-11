package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setup(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	root := t.TempDir()
	for rel, content := range map[string]string{
		"acme/tools/v0.1.0/edelweiss-ext-x_0.1.0_linux_amd64":           "binary one",
		"acme/tools/v0.1.0/checksums.txt":                               "sums",
		"acme/tools/v0.2.0/edelweiss-ext-x_0.2.0_linux_amd64":           "binary two",
		"acme/tools/v0.2.0/.hidden":                                     "not an asset",
		"acme/tools/v0.3.0-rc.1/edelweiss-ext-x_0.3.0-rc.1_linux_amd64": "candidate",
		"acme/tools/not-semver/file":                                    "x",
	} {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(newHandler(root))
	t.Cleanup(srv.Close)
	return srv, root
}

func get(t *testing.T, url, accept string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

func decode[T any](t *testing.T, body []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return v
}

func TestLatestSkipsPrereleasesAndHiddenFiles(t *testing.T) {
	srv, _ := setup(t)
	code, body := get(t, srv.URL+"/repos/acme/tools/releases/latest", "")
	rel := decode[releaseJSON](t, body)
	if code != 200 || rel.TagName != "v0.2.0" || rel.Prerelease || rel.PublishedAt.IsZero() || len(rel.Assets) != 1 {
		t.Fatalf("latest: %d %+v", code, rel)
	}
	a := rel.Assets[0]
	if a.Name != "edelweiss-ext-x_0.2.0_linux_amd64" || a.Size != 10 || !strings.HasPrefix(a.URL, srv.URL+"/repos/acme/tools/releases/assets/") ||
		a.BrowserDownloadURL != srv.URL+"/download/acme/tools/v0.2.0/edelweiss-ext-x_0.2.0_linux_amd64" {
		t.Fatalf("asset %+v", a)
	}
}

func TestTagsAndPrereleases(t *testing.T) {
	srv, _ := setup(t)
	code, body := get(t, srv.URL+"/repos/acme/tools/releases/tags/v0.3.0-rc.1", "")
	if rel := decode[releaseJSON](t, body); code != 200 || !rel.Prerelease || rel.TagName != "v0.3.0-rc.1" {
		t.Fatalf("prerelease: %d %+v", code, rel)
	}
	code, body = get(t, srv.URL+"/repos/acme/tools/releases/tags/v0.1.0", "")
	if rel := decode[releaseJSON](t, body); code != 200 || len(rel.Assets) != 2 {
		t.Fatalf("v0.1.0: %d %+v", code, rel)
	}
}

func TestLatestFileOverrides(t *testing.T) {
	srv, root := setup(t)
	if err := os.WriteFile(filepath.Join(root, "acme/tools/latest"), []byte("v0.1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, body := get(t, srv.URL+"/repos/acme/tools/releases/latest", "")
	if rel := decode[releaseJSON](t, body); rel.TagName != "v0.1.0" {
		t.Fatalf("latest file ignored: %+v", rel)
	}
}

func TestAssetDownloads(t *testing.T) {
	srv, _ := setup(t)
	_, body := get(t, srv.URL+"/repos/acme/tools/releases/latest", "")
	a := decode[releaseJSON](t, body).Assets[0]
	if code, data := get(t, a.URL, "application/octet-stream"); code != 200 || string(data) != "binary two" {
		t.Fatalf("asset bytes: %d %q", code, data)
	}
	if code, data := get(t, a.URL, "application/vnd.github+json"); code != 200 || decode[assetJSON](t, data).Name != a.Name {
		t.Fatalf("asset metadata: %d %s", code, data)
	}
	if code, data := get(t, a.BrowserDownloadURL, ""); code != 200 || string(data) != "binary two" {
		t.Fatalf("browser download: %d %q", code, data)
	}
}

func TestNotFound(t *testing.T) {
	srv, _ := setup(t)
	for _, p := range []string{
		"/repos/acme/missing/releases/latest",
		"/repos/acme/tools/releases/tags/v9.9.9",
		"/repos/acme/tools/releases/assets/12345",
		"/repos/acme/tools/releases/tags/..%2F..%2Fetc",
		"/repos/acme/tools/releases/tags/.hidden",
		"/download/acme/tools/v0.2.0/.hidden",
		"/download/acme/tools/v0.2.0/..%2Fv0.1.0%2Fchecksums.txt",
		"/elsewhere",
	} {
		code, body := get(t, srv.URL+p, "application/octet-stream")
		if code != 404 || !strings.Contains(string(body), `"message":"Not Found"`) {
			t.Errorf("%s: %d %s", p, code, body)
		}
	}
}
