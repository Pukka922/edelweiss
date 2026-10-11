package testutil

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Pukka922/edelweiss/core/internal/github"
)

// ReleaseServer is a fake GitHub release API: GET /repos/{owner}/{repo}/releases/latest|tags/{tag} and asset
// downloads at /repos/{owner}/{repo}/releases/assets/{id}.
type ReleaseServer struct {
	URL string

	mu       sync.Mutex
	releases map[string]map[string]github.Release // repo → tag → release
	latest   map[string]string                    // repo → tag
	files    map[int64]releaseFile
	nextID   int64
	gate     chan struct{}
	started  chan struct{}
}

type releaseFile struct {
	name string
	data []byte
}

// NewReleaseServer starts the server; it is closed when the test ends.
func NewReleaseServer(t testing.TB) *ReleaseServer {
	t.Helper()
	rs := &ReleaseServer{releases: map[string]map[string]github.Release{}, latest: map[string]string{}, files: map[int64]releaseFile{}}
	srv := httptest.NewServer(http.HandlerFunc(rs.serve))
	t.Cleanup(srv.Close)
	rs.URL = srv.URL
	return rs
}

// Add publishes a release of repo ("owner/repo") with the given files (name → content) and makes it the latest.
func (rs *ReleaseServer) Add(repo, tag string, files map[string][]byte) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rel := github.Release{TagName: tag, Name: tag, HTMLURL: "https://github.com/" + repo + "/releases/tag/" + tag,
		PublishedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	for _, name := range slices.Sorted(maps.Keys(files)) {
		rs.nextID++
		rs.files[rs.nextID] = releaseFile{name: name, data: files[name]}
		rel.Assets = append(rel.Assets, github.Asset{ID: rs.nextID, Name: name, Size: int64(len(files[name])),
			URL: fmt.Sprintf("%s/repos/%s/releases/assets/%d", rs.URL, repo, rs.nextID)})
	}
	if rs.releases[repo] == nil {
		rs.releases[repo] = map[string]github.Release{}
	}
	rs.releases[repo][tag] = rel
	rs.latest[repo] = tag
}

// Gate makes downloads of assets other than checksum lists wait until release is called; started receives once
// per waiting download.
func (rs *ReleaseServer) Gate() (release func(), started <-chan struct{}) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.gate, rs.started = make(chan struct{}), make(chan struct{}, 16)
	var once sync.Once
	gate := rs.gate
	return func() { once.Do(func() { close(gate) }) }, rs.started
}

func (rs *ReleaseServer) serve(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/repos/"), "/")
	if len(parts) < 4 || parts[2] != "releases" {
		notFound(w)
		return
	}
	repo := parts[0] + "/" + parts[1]
	rs.mu.Lock()
	switch {
	case len(parts) == 4 && parts[3] == "latest", len(parts) >= 5 && parts[3] == "tags":
		tag := rs.latest[repo]
		if parts[3] == "tags" {
			tag = strings.Join(parts[4:], "/")
		}
		rel, ok := rs.releases[repo][tag]
		rs.mu.Unlock()
		if !ok {
			notFound(w)
			return
		}
		_ = json.NewEncoder(w).Encode(rel)
	case len(parts) == 5 && parts[3] == "assets":
		id, _ := strconv.ParseInt(parts[4], 10, 64)
		f, ok := rs.files[id]
		gate, started := rs.gate, rs.started
		rs.mu.Unlock()
		if !ok {
			notFound(w)
			return
		}
		if gate != nil && !github.IsChecksums(f.name) {
			started <- struct{}{}
			<-gate
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(f.data)
	default:
		rs.mu.Unlock()
		notFound(w)
	}
}

func notFound(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"message":"Not Found"}`))
}

// SHA256 is the lower-case hex digest of data.
func SHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Checksums renders files as a sha256sum listing.
func Checksums(files map[string][]byte) []byte {
	var b strings.Builder
	for _, name := range slices.Sorted(maps.Keys(files)) {
		fmt.Fprintf(&b, "%s  %s\n", SHA256(files[name]), name)
	}
	return []byte(b.String())
}

// AssetName is the release asset name of an extension binary for this machine's architecture.
func AssetName(id, version string) string {
	return fmt.Sprintf("edelweiss-ext-%s_%s_linux_%s", id, version, runtime.GOARCH)
}

// OtherArch is a supported architecture this machine does not have.
func OtherArch() string {
	if runtime.GOARCH == "arm64" {
		return "amd64"
	}
	return "arm64"
}
