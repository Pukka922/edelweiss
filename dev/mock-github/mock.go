package main

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

type assetJSON struct {
	URL                string `json:"url"`
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	ContentType        string `json:"content_type"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type releaseJSON struct {
	URL         string      `json:"url"`
	HTMLURL     string      `json:"html_url"`
	ID          int64       `json:"id"`
	TagName     string      `json:"tag_name"`
	Name        string      `json:"name"`
	Draft       bool        `json:"draft"`
	Prerelease  bool        `json:"prerelease"`
	PublishedAt time.Time   `json:"published_at"`
	Assets      []assetJSON `json:"assets"`
}

type handler struct{ root string }

func newHandler(root string) http.Handler {
	h := &handler{root: root}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/{owner}/{repo}/releases/latest", h.latest)
	mux.HandleFunc("GET /repos/{owner}/{repo}/releases/tags/{tag}", h.byTag)
	mux.HandleFunc("GET /repos/{owner}/{repo}/releases/assets/{id}", h.asset)
	mux.HandleFunc("GET /download/{owner}/{repo}/{tag}/{name}", h.download)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { notFound(w) })
	return mux
}

// element reports whether s is a safe single path element (no traversal, no hidden files).
func element(s string) bool {
	return s != "" && !strings.HasPrefix(s, ".") && !strings.ContainsAny(s, `/\`)
}

func (h *handler) repoDir(r *http.Request) (string, bool) {
	owner, repo := r.PathValue("owner"), r.PathValue("repo")
	if !element(owner) || !element(repo) {
		return "", false
	}
	return filepath.Join(h.root, owner, repo), true
}

// tags lists the release directories of a repository directory.
func tags(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && element(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}

func canonical(tag string) string {
	if !strings.HasPrefix(tag, "v") {
		return "v" + tag
	}
	return tag
}

func latestTag(dir string) string {
	if data, err := os.ReadFile(filepath.Join(dir, "latest")); err == nil {
		if t := strings.TrimSpace(string(data)); element(t) {
			return t
		}
	}
	best := ""
	for _, t := range tags(dir) {
		v := canonical(t)
		if !semver.IsValid(v) || semver.Prerelease(v) != "" {
			continue
		}
		if best == "" || semver.Compare(v, canonical(best)) > 0 {
			best = t
		}
	}
	return best
}

func (h *handler) latest(w http.ResponseWriter, r *http.Request) {
	dir, ok := h.repoDir(r)
	tag := ""
	if ok {
		tag = latestTag(dir)
	}
	h.release(w, r, dir, tag)
}

func (h *handler) byTag(w http.ResponseWriter, r *http.Request) {
	dir, ok := h.repoDir(r)
	tag := r.PathValue("tag")
	if !ok || !element(tag) {
		notFound(w)
		return
	}
	h.release(w, r, dir, tag)
}

func (h *handler) release(w http.ResponseWriter, r *http.Request, dir, tag string) {
	rel, ok := h.describe(r, dir, tag)
	if !ok {
		notFound(w)
		return
	}
	writeJSON(w, rel)
}

// describe builds the release JSON for <dir>/<tag>; URLs point at the host the request came to.
func (h *handler) describe(r *http.Request, dir, tag string) (releaseJSON, bool) {
	if dir == "" || !element(tag) {
		return releaseJSON{}, false
	}
	info, err := os.Stat(filepath.Join(dir, tag))
	if err != nil || !info.IsDir() {
		return releaseJSON{}, false
	}
	owner, repo := r.PathValue("owner"), r.PathValue("repo")
	base := "http://" + r.Host
	v := canonical(tag)
	rel := releaseJSON{
		URL:         fmt.Sprintf("%s/repos/%s/%s/releases/%d", base, owner, repo, id(owner, repo, tag, "")),
		HTMLURL:     fmt.Sprintf("%s/%s/%s/releases/tag/%s", base, owner, repo, tag),
		ID:          id(owner, repo, tag, ""),
		TagName:     tag,
		Name:        tag,
		Prerelease:  semver.IsValid(v) && semver.Prerelease(v) != "",
		PublishedAt: info.ModTime().UTC().Truncate(time.Second),
		Assets:      []assetJSON{},
	}
	entries, _ := os.ReadDir(filepath.Join(dir, tag))
	for _, e := range entries {
		fi, err := e.Info()
		if err != nil || !fi.Mode().IsRegular() || !element(e.Name()) {
			continue
		}
		aid := id(owner, repo, tag, e.Name())
		rel.Assets = append(rel.Assets, assetJSON{
			URL: fmt.Sprintf("%s/repos/%s/%s/releases/assets/%d", base, owner, repo, aid), ID: aid, Name: e.Name(),
			Size: fi.Size(), ContentType: "application/octet-stream",
			BrowserDownloadURL: fmt.Sprintf("%s/download/%s/%s/%s/%s", base, owner, repo, tag, e.Name()),
		})
	}
	return rel, true
}

// id is a stable identifier of a release (name "") or an asset that fits into a JSON number.
func id(owner, repo, tag, name string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(owner + "/" + repo + "/" + tag + "/" + name))
	return int64(h.Sum64() & (1<<53 - 1))
}

func (h *handler) asset(w http.ResponseWriter, r *http.Request) {
	dir, ok := h.repoDir(r)
	want, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if !ok || err != nil {
		notFound(w)
		return
	}
	for _, tag := range tags(dir) {
		rel, _ := h.describe(r, dir, tag)
		for _, a := range rel.Assets {
			if a.ID != want {
				continue
			}
			if strings.Contains(r.Header.Get("Accept"), "application/octet-stream") {
				serveFile(w, r, filepath.Join(dir, tag, a.Name))
			} else {
				writeJSON(w, a)
			}
			return
		}
	}
	notFound(w)
}

func (h *handler) download(w http.ResponseWriter, r *http.Request) {
	dir, ok := h.repoDir(r)
	tag, name := r.PathValue("tag"), r.PathValue("name")
	if !ok || !element(tag) || !element(name) {
		notFound(w)
		return
	}
	serveFile(w, r, filepath.Join(dir, tag, name))
}

func serveFile(w http.ResponseWriter, r *http.Request, path string) {
	f, err := os.Open(path)
	if err != nil {
		notFound(w)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		notFound(w)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func notFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"message":"Not Found","documentation_url":"https://docs.github.com/rest"}`))
}
