package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"unicode"
)

func TestParseSource(t *testing.T) {
	ok := map[string]Source{
		"acme/tools":                                        {Owner: "acme", Repo: "tools"},
		"acme/tools@v1.2.0":                                 {Owner: "acme", Repo: "tools", Tag: "v1.2.0"},
		"github.com/acme/tools":                             {Owner: "acme", Repo: "tools"},
		"https://github.com/acme/tools":                     {Owner: "acme", Repo: "tools"},
		"https://github.com/acme/tools/":                    {Owner: "acme", Repo: "tools"},
		"https://github.com/acme/tools.git":                 {Owner: "acme", Repo: "tools"},
		"https://github.com/acme/tools@ext/v2":              {Owner: "acme", Repo: "tools", Tag: "ext/v2"},
		"https://github.com/acme/tools/releases/tag/v1.0.0": {Owner: "acme", Repo: "tools", Tag: "v1.0.0"},
		"https://github.com/acme/tools/releases/tag/ext/v2": {Owner: "acme", Repo: "tools", Tag: "ext/v2"},
		"https://github.com/acme/tools/releases/tag/a/b/c/": {Owner: "acme", Repo: "tools", Tag: "a/b/c"},
		"  acme/edelweiss-ext.docker  ":                     {Owner: "acme", Repo: "edelweiss-ext.docker"},
	}
	for in, want := range ok {
		got, err := ParseSource(in)
		if err != nil || got != want {
			t.Errorf("ParseSource(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "acme", "acme/", "/tools", "acme/tools/extra", "acme/tools@", "-acme/tools",
		"acme/..", "acme/tools@v1 2", "acme/tools@../x", "https://gitlab.com/acme/tools", "./tools", "acme/to ols",
		"acme/tools@.", "acme/tools@/v1", "acme/tools@v1/", "acme/tools@-v1", "acme/tools@ext//..",
		"https://github.com/acme/tools/releases/tag/.", "https://github.com/acme/tools/releases/tag//v1",
		"https://github.com/acme/tools/releases/tag/-v1", "https://github.com/acme/tools/releases/tag/a/../b",
		"https://github.com/acme/tools/releases/tag/", "https://github.com/acme/tools/releases/tag/v1@v2"} {
		if _, err := ParseSource(in); !errors.Is(err, ErrInvalidSource) {
			t.Errorf("ParseSource(%q): want ErrInvalidSource, got %v", in, err)
		}
	}
	s := Source{Owner: "acme", Repo: "tools", Tag: "v1"}
	if s.String() != "acme/tools@v1" || s.Repository() != "acme/tools" || (Source{Owner: "a", Repo: "b"}).String() != "a/b" {
		t.Fatalf("String/Repository: %s %s", s.String(), s.Repository())
	}
}

func TestSourceValidate(t *testing.T) {
	for _, s := range []Source{{Owner: "acme", Repo: "tools"}, {Owner: "acme", Repo: "tools", Tag: "ext/v2"}} {
		if err := s.Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v", s, err)
		}
	}
	for _, s := range []Source{{}, {Owner: "..", Repo: "x"}, {Owner: "acme"}, {Repo: "tools"}, {Owner: "acme", Repo: ".."},
		{Owner: "acme", Repo: "a/b"}, {Owner: "acme", Repo: "tools", Tag: "."}, {Owner: "acme", Repo: "tools", Tag: "../x"},
		{Owner: "acme", Repo: "tools", Tag: "/x"}, {Owner: "acme", Repo: "tools", Tag: "x/"},
		{Owner: "acme", Repo: "tools", Tag: "-x"}, {Owner: "acme", Repo: "tools", Tag: "a b"}} {
		if err := s.Validate(); !errors.Is(err, ErrInvalidSource) {
			t.Errorf("Validate(%+v) = %v, want ErrInvalidSource", s, err)
		}
	}
}

func newClient(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "secret-token", "edelweiss/test"), srv
}

func TestReleaseLatestAndTag(t *testing.T) {
	var paths []string
	var got http.Header
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.EscapedPath())
		got = r.Header.Clone()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v1.0.0", "name": "First", "html_url": "https://example/rel", "published_at": "2026-10-01T12:00:00Z",
			"assets": []map[string]any{{"id": 7, "name": "a", "size": 3, "url": "u", "browser_download_url": "b"}},
		})
	})
	rel, err := c.Release(context.Background(), Source{Owner: "acme", Repo: "tools"})
	if err != nil {
		t.Fatal(err)
	}
	if rel.TagName != "v1.0.0" || rel.Name != "First" || len(rel.Assets) != 1 || rel.Assets[0].ID != 7 || rel.Assets[0].URL != "u" || rel.PublishedAt.IsZero() {
		t.Fatalf("release %+v", rel)
	}
	if _, err := c.Release(context.Background(), Source{Owner: "acme", Repo: "tools", Tag: "ext/v2"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"/repos/acme/tools/releases/latest", "/repos/acme/tools/releases/tags/ext%2Fv2"}; !slices.Equal(paths, want) {
		t.Fatalf("paths %v, want %v", paths, want)
	}
	for k, v := range map[string]string{
		"Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28",
		"User-Agent": "edelweiss/test", "Authorization": "Bearer secret-token",
	} {
		if got.Get(k) != v {
			t.Errorf("header %s = %q, want %q", k, got.Get(k), v)
		}
	}
}

func TestReleaseErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		headers map[string]string
		body    string
		want    error
		text    string
	}{
		{"not found", 404, nil, `{"message":"Not Found"}`, ErrReleaseNotFound, "acme/tools"},
		{"rate limited 403", 403, map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1790000000"}, `{}`, ErrRateLimited, "token"},
		{"rate limited 429", 429, nil, `{}`, ErrRateLimited, "token"},
		{"forbidden", 403, nil, `{"message":"Resource not accessible"}`, ErrUnavailable, "Resource not accessible"},
		{"server error", 502, nil, `bad gateway`, ErrUnavailable, "502"},
		{"bad json", 200, nil, `{"tag_name":`, ErrUnavailable, "invalid release"},
		{"no tag", 200, nil, `{"name":"x"}`, ErrUnavailable, "tag_name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tc.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			_, err := c.Release(context.Background(), Source{Owner: "acme", Repo: "tools"})
			if !errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.text) {
				t.Fatalf("got %v, want %v containing %q", err, tc.want, tc.text)
			}
		})
	}
	unreachable := NewClient("http://127.0.0.1:1", "", "x")
	if _, err := unreachable.Release(context.Background(), Source{Owner: "a", Repo: "b"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unreachable: %v", err)
	}
}

// A deadline that passes during a request is still ErrUnavailable, but the context error stays in the chain, so that
// callers can tell a timeout from a network failure.
func TestExpiredContextKeepsTheContextError(t *testing.T) {
	c, _ := newClient(t, func(http.ResponseWriter, *http.Request) {})
	ctx, cancel := context.WithTimeout(context.Background(), 0) // expired
	defer cancel()
	_, err := c.Release(ctx, Source{Owner: "acme", Repo: "tools"})
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Release: %v", err)
	}
	_, err = c.Download(ctx, Asset{Name: "a", URL: c.APIURL + "/assets/1"}, io.Discard, 10)
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Download: %v", err)
	}
}

func TestReleaseValidatesSource(t *testing.T) {
	var requests atomic.Int32
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) { requests.Add(1) })
	for _, src := range []Source{{Owner: "..", Repo: "x"}, {Owner: "acme", Repo: ".."}, {Owner: "acme", Repo: "tools", Tag: "../../x"}, {}} {
		if _, err := c.Release(context.Background(), src); !errors.Is(err, ErrInvalidSource) {
			t.Errorf("Release(%+v) = %v, want ErrInvalidSource", src, err)
		}
	}
	if n := requests.Load(); n != 0 {
		t.Fatalf("%d requests for invalid sources, want none", n)
	}
}

func TestReleaseSameHostRedirectKeepsToken(t *testing.T) {
	// GitHub answers a renamed repository with a 301 to another path on the same API host.
	var seen []string
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path+" "+r.Header.Get("Authorization"))
		if r.URL.Path == "/repos/acme/old/releases/latest" {
			http.Redirect(w, r, "/repositories/42/releases/latest", http.StatusMovedPermanently)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v1.0.0"})
	})
	if _, err := c.Release(context.Background(), Source{Owner: "acme", Repo: "old"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"/repos/acme/old/releases/latest Bearer secret-token", "/repositories/42/releases/latest Bearer secret-token"}
	if !slices.Equal(seen, want) {
		t.Fatalf("requests %q, want %q", seen, want)
	}
}

func TestReleaseErrorMessage(t *testing.T) {
	serve := func(t *testing.T, status int, body string) error {
		t.Helper()
		c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		})
		_, err := c.Release(context.Background(), Source{Owner: "acme", Repo: "tools"})
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("got %v, want ErrUnavailable", err)
		}
		return err
	}

	// A hostile or verbose server must not flood the error or smuggle terminal escapes into it.
	raw, err := json.Marshal(map[string]string{"message": "bad\n\x1b[31mred\x07" + strings.Repeat("é", 500)})
	if err != nil {
		t.Fatal(err)
	}
	msg := serve(t, 500, string(raw)).Error()
	if strings.ContainsFunc(msg, unicode.IsControl) {
		t.Errorf("control characters in %q", msg)
	}
	if want := " bad[31mred" + strings.Repeat("é", 190); !strings.HasSuffix(msg, want) {
		t.Errorf("message not cut to 200 characters: %q", msg)
	}

	// No message from the server: nothing is appended, not even a space.
	for _, body := range []string{`bad gateway`, `{}`, `{"message":""}`} {
		if msg := serve(t, 502, body).Error(); !strings.HasSuffix(msg, "HTTP 502") {
			t.Errorf("body %q: error %q should end with the status", body, msg)
		}
	}
}

func TestDownload(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 100)
	var auth, accept []string
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		_, _ = w.Write(payload)
	}))
	t.Cleanup(storage.Close)
	c, srv := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		accept = append(accept, r.Header.Get("Accept"))
		switch r.URL.Path {
		case "/assets/1":
			_, _ = w.Write(payload)
		case "/assets/2": // GitHub redirects asset downloads to its storage host
			http.Redirect(w, r, storage.URL+"/blob", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()

	var buf bytes.Buffer
	if n, err := c.Download(ctx, Asset{Name: "a", Size: 100, URL: srv.URL + "/assets/1"}, &buf, 100); err != nil || n != 100 || buf.Len() != 100 {
		t.Fatalf("download: %d %v", n, err)
	}
	if accept[0] != "application/octet-stream" || auth[0] != "Bearer secret-token" {
		t.Fatalf("headers: accept %v auth %v", accept, auth)
	}

	auth = nil
	buf.Reset()
	if _, err := c.Download(ctx, Asset{Name: "b", Size: 100, URL: srv.URL + "/assets/2"}, &buf, 1000); err != nil || buf.Len() != 100 {
		t.Fatalf("redirected download: %v", err)
	}
	if len(auth) != 2 || auth[0] != "Bearer secret-token" || auth[1] != "" {
		t.Fatalf("the token must not follow a redirect to another host: %q", auth)
	}

	auth = nil
	if _, err := c.Download(ctx, Asset{Name: "c", URL: storage.URL + "/direct"}, &bytes.Buffer{}, 1000); err != nil {
		t.Fatal(err)
	}
	if len(auth) != 1 || auth[0] != "" {
		t.Fatalf("the token was sent to a host that is not the API: %q", auth)
	}

	if _, err := c.Download(ctx, Asset{Name: "big", Size: 101, URL: srv.URL + "/assets/1"}, &bytes.Buffer{}, 100); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("announced size over the limit: %v", err)
	}
	if _, err := c.Download(ctx, Asset{Name: "lies", Size: 10, URL: srv.URL + "/assets/1"}, &bytes.Buffer{}, 50); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("actual size over the limit: %v", err)
	}
	if _, err := c.Download(ctx, Asset{Name: "gone", URL: srv.URL + "/missing"}, &bytes.Buffer{}, 50); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing asset: %v", err)
	}
}

func TestDownloadLimit(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 100)
	var requests atomic.Int32
	c, srv := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write(payload)
	})
	ctx := context.Background()

	// A limit that cannot hold any data is a caller bug, not an oversized download.
	for _, limit := range []int64{0, -1, math.MinInt64} {
		var buf bytes.Buffer
		n, err := c.Download(ctx, Asset{Name: "a", URL: srv.URL + "/assets/1"}, &buf, limit)
		if err == nil || errors.Is(err, ErrTooLarge) || n != 0 || buf.Len() != 0 {
			t.Errorf("limit %d: %d bytes, error %v; want a plain error", limit, n, err)
		}
	}
	if n := requests.Load(); n != 0 {
		t.Fatalf("%d requests for an invalid limit, want none", n)
	}

	// limit+1 must not overflow: the maximum limit means "no limit" and the data still arrives.
	var buf bytes.Buffer
	if n, err := c.Download(ctx, Asset{Name: "max", Size: 100, URL: srv.URL + "/assets/1"}, &buf, math.MaxInt64); err != nil || n != 100 || buf.Len() != 100 {
		t.Fatalf("limit MaxInt64: %d bytes, %d written, error %v", n, buf.Len(), err)
	}
}

// The name of a checksum list is any string that ends in "checksums.txt", and the Download errors reach the terminal
// before anything is confirmed: the name must be printed quoted, with control and escape characters escaped.
func TestDownloadErrorsQuoteAssetNames(t *testing.T) {
	const name = "evil\x1b[2J\x07checksums.txt"
	quoted := `"evil\x1b[2J\achecksums.txt"`
	c, srv := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/assets/ok":
			_, _ = w.Write(bytes.Repeat([]byte("x"), 100))
		case "/assets/cut": // announces 100 bytes and delivers 10
			w.Header().Set("Content-Length", "100")
			_, _ = w.Write(bytes.Repeat([]byte("x"), 10))
		default:
			http.NotFound(w, r)
		}
	})
	for _, tc := range []struct {
		what  string
		asset Asset
		limit int64
	}{
		{"a limit that cannot hold data", Asset{Name: name, URL: srv.URL + "/assets/ok"}, 0},
		{"an announced size over the limit", Asset{Name: name, Size: 101, URL: srv.URL + "/assets/ok"}, 100},
		{"no download URL", Asset{Name: name}, 100},
		{"an HTTP error", Asset{Name: name, URL: srv.URL + "/missing"}, 100},
		{"a transfer that breaks off", Asset{Name: name, URL: srv.URL + "/assets/cut"}, 1000},
		{"more data than the limit", Asset{Name: name, Size: 10, URL: srv.URL + "/assets/ok"}, 50},
	} {
		_, err := c.Download(context.Background(), tc.asset, io.Discard, tc.limit)
		switch {
		case err == nil:
			t.Errorf("%s: no error", tc.what)
		case strings.ContainsAny(err.Error(), "\x1b\x07"):
			t.Errorf("%s: the error carries raw control characters: %q", tc.what, err.Error())
		case !strings.Contains(err.Error(), quoted):
			t.Errorf("%s: the error does not show the quoted name %s: %q", tc.what, quoted, err.Error())
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDownloadTokenNeedsSameScheme(t *testing.T) {
	// httptest cannot serve http and https on one host:port, so the transport is replaced and the requests recorded.
	var seen []string
	c := NewClient("https://api.example.test", "secret-token", "x")
	c.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		seen = append(seen, r.URL.String()+" auth="+r.Header.Get("Authorization"))
		resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("ok")), Request: r}
		if r.URL.Path == "/assets/downgrade" {
			resp.StatusCode, resp.Body = http.StatusFound, http.NoBody
			resp.Header.Set("Location", "http://api.example.test/blob")
		}
		return resp, nil
	})
	for _, tc := range []struct {
		url  string
		want []string
	}{
		{"https://api.example.test/assets/1", []string{"https://api.example.test/assets/1 auth=Bearer secret-token"}},
		{"http://api.example.test/assets/1", []string{"http://api.example.test/assets/1 auth="}},
		{"https://api.example.test/assets/downgrade", []string{
			"https://api.example.test/assets/downgrade auth=Bearer secret-token", "http://api.example.test/blob auth="}},
	} {
		seen = nil
		if _, err := c.Download(context.Background(), Asset{Name: "a", URL: tc.url}, io.Discard, 10); err != nil {
			t.Fatalf("%s: %v", tc.url, err)
		}
		if !slices.Equal(seen, tc.want) {
			t.Errorf("%s: requests %q, want %q", tc.url, seen, tc.want)
		}
	}
}

func TestParseAssetName(t *testing.T) {
	ok := map[string]AssetInfo{
		"edelweiss-ext-system_0.1.0_linux_amd64":              {ID: "system", Version: "0.1.0", Arch: "amd64"},
		"edelweiss-ext-docker-compose_1.2.3-rc.1_linux_arm64": {ID: "docker-compose", Version: "1.2.3-rc.1", Arch: "arm64"},
		"edelweiss-ext-hello_0.2.0_linux_amd64.tar.gz":        {ID: "hello", Version: "0.2.0", Arch: "amd64", Archive: true},
	}
	for name, want := range ok {
		if got, ok := ParseAssetName(name); !ok || got != want {
			t.Errorf("ParseAssetName(%q) = %+v, %v", name, got, ok)
		}
	}
	for _, name := range []string{"edelweissd_0.1.0_linux_amd64", "edelweiss-ext-system_0.1.0_linux_386",
		"edelweiss-ext-system_v0.1.0_linux_amd64", "edelweiss-ext-system_0.1.0_darwin_arm64",
		"edelweiss-ext-system_0.1.0_linux_amd64.zip", "edelweiss-ext-Sys_0.1.0_linux_amd64", "checksums.txt"} {
		if _, ok := ParseAssetName(name); ok {
			t.Errorf("ParseAssetName(%q) accepted", name)
		}
	}
	if !IsChecksums("checksums.txt") || !IsChecksums("edelweiss-ext-hello_0.1.0_checksums.txt") || IsChecksums("checksums.txt.sig") {
		t.Fatal("IsChecksums")
	}
}

func TestParseChecksums(t *testing.T) {
	a, b := strings.Repeat("a", 64), strings.Repeat("B", 64)
	got, err := ParseChecksums(strings.NewReader(a + "  edelweiss-ext-x_1.0.0_linux_amd64\n\n" + b + " *other file.tar.gz\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["edelweiss-ext-x_1.0.0_linux_amd64"] != a || got["other file.tar.gz"] != strings.ToLower(b) {
		t.Fatalf("checksums %v", got)
	}
	for _, bad := range []string{"abc  file\n", strings.Repeat("g", 64) + "  file\n", a + "\n"} {
		if _, err := ParseChecksums(strings.NewReader(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestParseChecksumsSizeLimit(t *testing.T) {
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	line := a + "  f\n"
	padding := strings.Repeat("\n", MaxChecksumsSize-len(line))

	got, err := ParseChecksums(strings.NewReader(line + padding))
	if err != nil || got["f"] != a {
		t.Fatalf("a list of exactly MaxChecksumsSize bytes: %v, %v", got, err)
	}
	// An entry behind the limit must fail the whole list, not vanish silently.
	if got, err := ParseChecksums(strings.NewReader(line + padding + b + "  g\n")); err == nil {
		t.Fatalf("a list over MaxChecksumsSize was accepted: %v", got)
	}
}

func TestParseChecksumsDuplicates(t *testing.T) {
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	for _, in := range []string{a + "  f\n" + a + "  f\n", a + "  f\n" + strings.ToUpper(a) + " *f\n"} {
		got, err := ParseChecksums(strings.NewReader(in))
		if err != nil || len(got) != 1 || got["f"] != a {
			t.Errorf("same digest twice %q: %v, %v", in, got, err)
		}
	}
	if got, err := ParseChecksums(strings.NewReader(a + "  f\n" + b + "  f\n")); err == nil || !strings.Contains(err.Error(), `"f"`) {
		t.Errorf("conflicting digests for one name: %v, %v", got, err)
	}
}

// A file name comes from the publisher and reaches the terminal in the error before anything is confirmed, so it
// must be printed quoted, with control and escape characters escaped.
func TestParseChecksumsQuotesNamesInErrors(t *testing.T) {
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	name := "evil\x1b[2J\x07name"
	_, err := ParseChecksums(strings.NewReader(a + "  " + name + "\n" + b + "  " + name + "\n"))
	if err == nil {
		t.Fatal("conflicting digests for one name were accepted")
	}
	if strings.ContainsAny(err.Error(), "\x1b\x07") {
		t.Errorf("the error carries raw control characters: %q", err.Error())
	}
	if want := `"evil\x1b[2J\aname"`; !strings.Contains(err.Error(), want) {
		t.Errorf("the error does not show the quoted name %s: %q", want, err.Error())
	}
}
