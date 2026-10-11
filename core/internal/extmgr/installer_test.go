package extmgr

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/Pukka922/edelweiss/core/internal/github"
	"github.com/Pukka922/edelweiss/core/internal/testutil"
)

func readBin(t *testing.T, version string) []byte {
	t.Helper()
	p, err := testutil.BuildTestExtVersion(version)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// withChecksums adds a checksums.txt listing every file.
func withChecksums(files map[string][]byte) map[string][]byte {
	files["checksums.txt"] = testutil.Checksums(files)
	return files
}

func newInstaller(t *testing.T, rs *testutil.ReleaseServer) (*Installer, *Manager) {
	t.Helper()
	m, _ := newManager(t, testutil.TempDir(t), nil)
	return NewInstaller(InstallerOptions{Manager: m, Releases: github.NewClient(rs.URL, "", "test"),
		Arch: runtime.GOARCH, MaxDownload: 200 << 20}), m
}

// captureLog makes inst log into the returned buffer (the installer logs from the calling goroutine only).
func captureLog(inst *Installer) *bytes.Buffer {
	var buf bytes.Buffer
	inst.opts.Logger = slog.New(slog.NewTextHandler(&buf, nil))
	return &buf
}

// countingReleases counts the downloads an Installer makes.
type countingReleases struct {
	Releases
	downloads int
}

func (c *countingReleases) Download(ctx context.Context, a github.Asset, w io.Writer, limit int64) (int64, error) {
	c.downloads++
	return c.Releases.Download(ctx, a, w, limit)
}

type tarEntry struct {
	name string
	kind byte
	mode int64
	data []byte
}

func tarGz(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.kind, Mode: e.mode, Size: int64(len(e.data)), ModTime: time.Now()}
		if e.kind == tar.TypeSymlink {
			h.Linkname, h.Size = "target", 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func assertTmpEmpty(t *testing.T, m *Manager) {
	t.Helper()
	entries, err := os.ReadDir(m.opts.TmpDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("leftovers in the tmp dir: %v %v", entries, err)
	}
}

func TestResolve(t *testing.T) {
	rs := testutil.NewReleaseServer(t)
	bin := readBin(t, "")
	// only this architecture's extension asset is a candidate
	rs.Add("acme/tools", "v1.0.0", withChecksums(map[string][]byte{
		testutil.AssetName("testext", "1.0.0"):                      bin,
		"edelweiss-ext-testext_1.0.0_linux_" + testutil.OtherArch(): []byte("other architecture"),
		"edelweissd_1.0.0_linux_" + runtime.GOARCH:                  []byte("the core"),
		"README.md": []byte("hi"),
	}))
	inst, _ := newInstaller(t, rs)

	res, err := inst.Resolve(context.Background(), "https://github.com/acme/tools")
	if err != nil {
		t.Fatal(err)
	}
	want := Candidate{ID: "testext", Version: "1.0.0", Arch: runtime.GOARCH, Asset: testutil.AssetName("testext", "1.0.0"),
		Size: int64(len(bin)), SHA256: testutil.SHA256(bin)}
	if res.Repo != "acme/tools" || res.Tag != "v1.0.0" || res.URL == "" || res.PublishedAt.IsZero() || len(res.Candidates) != 1 {
		t.Fatalf("resolution %+v", res)
	}
	got := res.Candidates[0]
	got.asset = github.Asset{}
	if got != want {
		t.Fatalf("candidate %+v\nwant      %+v", got, want)
	}
}

func TestResolveErrors(t *testing.T) {
	rs := testutil.NewReleaseServer(t)
	rs.Add("acme/none", "v1", withChecksums(map[string][]byte{"README.md": []byte("x"),
		"edelweiss-ext-demo_1.0.0_linux_" + testutil.OtherArch(): []byte("x")}))
	rs.Add("acme/nosums", "v1", map[string][]byte{testutil.AssetName("demo", "1.0.0"): []byte("x")})
	rs.Add("acme/unlisted", "v1", map[string][]byte{testutil.AssetName("demo", "1.0.0"): []byte("x"),
		"checksums.txt": testutil.Checksums(map[string][]byte{"other": []byte("y")})})
	rs.Add("acme/badsums", "v1", map[string][]byte{testutil.AssetName("demo", "1.0.0"): []byte("x"), "checksums.txt": []byte("garbage\n")})
	inst, _ := newInstaller(t, rs)
	ctx := context.Background()
	for source, want := range map[string]error{
		"acme/none":         ErrNoCandidates,
		"acme/nosums":       ErrNoChecksums,
		"acme/unlisted":     ErrNoChecksums,
		"acme/badsums":      ErrNoChecksums,
		"acme/missing":      github.ErrReleaseNotFound,
		"acme/none@v9":      github.ErrReleaseNotFound,
		"not a repository!": github.ErrInvalidSource,
	} {
		if _, err := inst.Resolve(ctx, source); !errors.Is(err, want) {
			t.Errorf("Resolve(%q) = %v, want %v", source, err, want)
		}
	}
	// the error names what is not listed
	if _, err := inst.Resolve(ctx, "acme/unlisted"); err == nil || !strings.Contains(err.Error(), testutil.AssetName("demo", "1.0.0")) {
		t.Errorf("unlisted: %v does not name the asset", err)
	}
}

// TestResolveOffersListedAssets: only assets a checksum list vouches for are candidates, one per extension id (the
// highest version; for the same version the raw binary over the archive).
func TestResolveOffersListedAssets(t *testing.T) {
	raw := func(id, version string) string { return testutil.AssetName(id, version) }
	archive := func(id, version string) string { return testutil.AssetName(id, version) + ".tar.gz" }
	for name, tc := range map[string]struct {
		listed, unlisted []string
		want             []string // the assets of the candidates, ordered by id
	}{
		"an unlisted extension is dropped":           {listed: []string{raw("testext", "1.0.0")}, unlisted: []string{raw("zeta", "1.0.0")}, want: []string{raw("testext", "1.0.0")}},
		"a listed archive beats an unlisted binary":  {listed: []string{archive("testext", "1.0.0")}, unlisted: []string{raw("testext", "1.0.0")}, want: []string{archive("testext", "1.0.0")}},
		"a listed binary beats a listed archive":     {listed: []string{archive("testext", "1.0.0"), raw("testext", "1.0.0")}, want: []string{raw("testext", "1.0.0")}},
		"an unlisted higher version is ignored":      {listed: []string{raw("testext", "1.0.0")}, unlisted: []string{raw("testext", "2.0.0")}, want: []string{raw("testext", "1.0.0")}},
		"the highest of several versions wins":       {listed: []string{raw("testext", "1.0.0"), raw("testext", "1.10.0"), raw("testext", "1.9.0")}, want: []string{raw("testext", "1.10.0")}},
		"a newer archive beats an older raw binary":  {listed: []string{raw("testext", "1.0.0"), archive("testext", "1.1.0")}, want: []string{archive("testext", "1.1.0")}},
		"several extensions are ordered by their id": {listed: []string{raw("zeta", "1.0.0"), raw("alpha", "1.0.0")}, want: []string{raw("alpha", "1.0.0"), raw("zeta", "1.0.0")}},
	} {
		t.Run(name, func(t *testing.T) {
			files, listed := map[string][]byte{}, map[string][]byte{}
			for _, n := range tc.listed {
				files[n], listed[n] = []byte("content of "+n), []byte("content of "+n)
			}
			for _, n := range tc.unlisted {
				files[n] = []byte("content of " + n)
			}
			files["checksums.txt"] = testutil.Checksums(listed)
			rs := testutil.NewReleaseServer(t)
			rs.Add("acme/tools", "v1", files)
			inst, _ := newInstaller(t, rs)
			res, err := inst.Resolve(context.Background(), "acme/tools")
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, c := range res.Candidates {
				got = append(got, c.Asset)
				if c.SHA256 != testutil.SHA256(files[c.Asset]) || c.Archive != strings.HasSuffix(c.Asset, ".tar.gz") {
					t.Errorf("candidate %+v does not match its asset", c)
				}
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("candidates %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResolveChecksumLists(t *testing.T) {
	ctx := context.Background()
	a, z := testutil.AssetName("alpha", "1.0.0"), testutil.AssetName("zeta", "1.0.0")
	assets := map[string][]byte{a: []byte("alpha"), z: []byte("zeta")}
	list := func(names ...string) []byte {
		files := map[string][]byte{}
		for _, n := range names {
			files[n] = assets[n]
		}
		return testutil.Checksums(files)
	}
	release := func(t *testing.T, lists map[string][]byte) (*Installer, *countingReleases) {
		t.Helper()
		files := map[string][]byte{a: assets[a], z: assets[z]}
		for n, data := range lists {
			files[n] = data
		}
		rs := testutil.NewReleaseServer(t)
		rs.Add("acme/tools", "v1", files)
		inst, _ := newInstaller(t, rs)
		counter := &countingReleases{Releases: inst.opts.Releases}
		inst.opts.Releases = counter
		return inst, counter
	}

	t.Run("lists are merged", func(t *testing.T) {
		inst, _ := release(t, map[string][]byte{"checksums.txt": list(a), "zeta_1.0.0_checksums.txt": list(z)})
		res, err := inst.Resolve(ctx, "acme/tools")
		if err != nil || len(res.Candidates) != 2 {
			t.Fatalf("%+v %v", res, err)
		}
	})
	t.Run("lists that disagree", func(t *testing.T) {
		other := testutil.Checksums(map[string][]byte{a: []byte("not alpha")})
		inst, _ := release(t, map[string][]byte{"checksums.txt": list(a, z), "other_checksums.txt": other})
		if _, err := inst.Resolve(ctx, "acme/tools"); !errors.Is(err, ErrNoChecksums) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("lists may disagree about files that are not candidates", func(t *testing.T) {
		one := testutil.Checksums(map[string][]byte{"README.md": []byte("1")})
		two := testutil.Checksums(map[string][]byte{"README.md": []byte("2")})
		inst, _ := release(t, map[string][]byte{"checksums.txt": list(a, z), "a_checksums.txt": one, "b_checksums.txt": two})
		if res, err := inst.Resolve(ctx, "acme/tools"); err != nil || len(res.Candidates) != 2 {
			t.Fatalf("%+v %v", res, err)
		}
	})
	t.Run("at most eight lists are downloaded", func(t *testing.T) {
		lists := map[string][]byte{"checksums.txt": list(a, z)}
		for k := 1; k < maxChecksumLists; k++ {
			lists[fmt.Sprintf("part%d_checksums.txt", k)] = list(a)
		}
		inst, counter := release(t, lists)
		if res, err := inst.Resolve(ctx, "acme/tools"); err != nil || len(res.Candidates) != 2 || counter.downloads != maxChecksumLists {
			t.Fatalf("%d lists: %+v %v, %d downloads", len(lists), res, err, counter.downloads)
		}
		lists["one-too-many_checksums.txt"] = list(a)
		inst, counter = release(t, lists)
		if _, err := inst.Resolve(ctx, "acme/tools"); !errors.Is(err, ErrNoChecksums) || counter.downloads != 0 {
			t.Fatalf("%d lists: %v, %d downloads", len(lists), err, counter.downloads)
		}
	})
}

// The name of a checksum list is any string that ends in "checksums.txt" and the errors reach the terminal before
// anything is confirmed: the name must be printed quoted, with control and escape characters escaped.
func TestResolveQuotesChecksumListNames(t *testing.T) {
	const list = "evil\x1b[2J\x07checksums.txt"
	quoted := `"evil\x1b[2J\achecksums.txt"`
	asset := testutil.AssetName("alpha", "1.0.0")
	for name, content := range map[string][]byte{
		"a list that is not in sha256sum format": []byte("garbage\n"),
		"a list over the size limit":             bytes.Repeat([]byte("x"), github.MaxChecksumsSize+1),
	} {
		t.Run(name, func(t *testing.T) {
			rs := testutil.NewReleaseServer(t)
			rs.Add("acme/tools", "v1", map[string][]byte{asset: []byte("alpha"), list: content})
			inst, _ := newInstaller(t, rs)
			_, err := inst.Resolve(context.Background(), "acme/tools")
			switch {
			case err == nil:
				t.Fatal("no error")
			case strings.ContainsAny(err.Error(), "\x1b\x07"):
				t.Errorf("the error carries raw control characters: %q", err.Error())
			case !strings.Contains(err.Error(), quoted):
				t.Errorf("the error does not show the quoted name %s: %q", quoted, err.Error())
			}
		})
	}
}

// The tag of a release comes from the API answer, and it is shown before anything is confirmed (CLI, web). git accepts
// tags with a C1 control character (U+009B is a CSI) or a bidi override (U+202E), so a tag that would not pass as a
// source tag is refused as an unexpected answer of GitHub, by every way into resolve, and shown only quoted.
func TestResolveRefusesReleaseTagsThatAreNotSourceTags(t *testing.T) {
	ctx := context.Background()
	oldBin, newBin := readBin(t, ""), readBin(t, "1.1.0")
	for name, tag := range map[string]string{
		"C1 control character": "v1.1.0\u009b2K",
		"bidi override":        "v1.1.0\u202etxt",
		"newline":              "v1.1.0\nInstall?",
		"space":                "v 1.1.0",
		"dot dot":              "v1..1",
	} {
		t.Run(name, func(t *testing.T) {
			rs := testutil.NewReleaseServer(t)
			rs.Add("acme/tools", "v1.0.0", withChecksums(map[string][]byte{testutil.AssetName("testext", "1.0.0"): oldBin}))
			inst, m := newInstaller(t, rs)
			if _, err := inst.Install(ctx, InstallRequest{Source: "acme/tools@v1.0.0"}); err != nil {
				t.Fatal(err)
			}
			// the newest release has the bad tag
			rs.Add("acme/tools", tag, withChecksums(map[string][]byte{testutil.AssetName("testext", "1.1.0"): newBin}))

			refused := func(how string, err error) {
				t.Helper()
				if !errors.Is(err, github.ErrUnavailable) {
					t.Errorf("%s: %v, want an unexpected answer of GitHub (ErrUnavailable)", how, err)
					return
				}
				if want := fmt.Sprintf("%q", tag); !strings.Contains(err.Error(), want) {
					t.Errorf("%s: the error does not show the quoted tag %s: %q", how, want, err.Error())
				}
				for _, r := range err.Error() {
					if !unicode.IsPrint(r) {
						t.Errorf("%s: the error carries the unprintable character %U: %q", how, r, err.Error())
						break
					}
				}
			}
			res, err := inst.Resolve(ctx, "acme/tools")
			if res != nil {
				t.Errorf("Resolve returned the release: %+v", res)
			}
			refused("Resolve", err)
			_, err = inst.Install(ctx, InstallRequest{Source: "acme/tools"})
			refused("Install", err)
			updates, failed := inst.Updates(ctx)
			if len(updates) != 0 || len(failed) != 1 {
				t.Fatalf("Updates: %+v, failed %+v", updates, failed)
			}
			refused("Updates", failed[0].Err)
			_, err = inst.Update(ctx, "testext", "")
			refused("Update", err)

			if got, _ := m.Get("testext"); got == nil || got.Manifest.Version != "1.0.0" {
				t.Errorf("the installed extension changed: %+v", got)
			}
			assertTmpEmpty(t, m)
		})
	}

	t.Run("a normal tag still works", func(t *testing.T) {
		rs := testutil.NewReleaseServer(t)
		rs.Add("acme/tools", "release/v1.2.0+build.5_rc-1", withChecksums(map[string][]byte{testutil.AssetName("testext", "1.0.0"): oldBin}))
		inst, _ := newInstaller(t, rs)
		res, err := inst.Resolve(ctx, "acme/tools")
		if err != nil || res.Tag != "release/v1.2.0+build.5_rc-1" || len(res.Candidates) != 1 {
			t.Fatalf("%+v %v", res, err)
		}
	})
}

func TestInstallFromRelease(t *testing.T) {
	rs := testutil.NewReleaseServer(t)
	bin := readBin(t, "")
	asset := testutil.AssetName("testext", "1.0.0")
	rs.Add("acme/tools", "v1.0.0", withChecksums(map[string][]byte{asset: bin}))
	inst, m := newInstaller(t, rs)

	ext, err := inst.Install(context.Background(), InstallRequest{Source: "acme/tools@v1.0.0", SHA256: testutil.SHA256(bin)})
	if err != nil {
		t.Fatal(err)
	}
	src := ext.Source
	src.InstalledAt = time.Time{}
	want := Source{Type: "github", Location: "github.com/acme/tools", Repo: "acme/tools", Tag: "v1.0.0", Asset: asset, SHA256: testutil.SHA256(bin)}
	if ext.Manifest.ID != "testext" || src != want || ext.Source.InstalledAt.IsZero() {
		t.Fatalf("installed %+v, source %+v", ext.Manifest, ext.Source)
	}
	if got, ok := m.Get("testext"); !ok || got.Source.Repo != "acme/tools" {
		t.Fatalf("not in the store: %+v", got)
	}
	assertTmpEmpty(t, m)
}

func TestInstallFromArchive(t *testing.T) {
	bin := readBin(t, "")
	good := tarGz(t,
		tarEntry{name: "pkg/", kind: tar.TypeDir, mode: 0o755},
		tarEntry{name: "pkg/README.md", kind: tar.TypeReg, mode: 0o644, data: []byte("read me")},
		tarEntry{name: "pkg/edelweiss-ext-testext", kind: tar.TypeReg, mode: 0o755, data: bin})
	rs := testutil.NewReleaseServer(t)
	rs.Add("acme/tools", "v1.0.0", withChecksums(map[string][]byte{testutil.AssetName("testext", "1.0.0") + ".tar.gz": good}))
	inst, m := newInstaller(t, rs)
	if _, err := inst.Install(context.Background(), InstallRequest{Source: "acme/tools"}); err != nil {
		t.Fatal(err)
	}
	if ext, ok := m.Get("testext"); !ok || ext.Source.Asset != testutil.AssetName("testext", "1.0.0")+".tar.gz" {
		t.Fatalf("archive not installed: %+v", ext)
	}
	assertTmpEmpty(t, m)

	for name, archive := range map[string][]byte{
		"two executables": tarGz(t, tarEntry{name: "a", kind: tar.TypeReg, mode: 0o755, data: bin}, tarEntry{name: "b", kind: tar.TypeReg, mode: 0o755, data: bin}),
		"no executable":   tarGz(t, tarEntry{name: "README", kind: tar.TypeReg, mode: 0o644, data: []byte("x")}),
		"symlink":         tarGz(t, tarEntry{name: "link", kind: tar.TypeSymlink, mode: 0o777}),
		"not gzip":        []byte("plain text"),
	} {
		rs := testutil.NewReleaseServer(t)
		rs.Add("acme/tools", "v1.0.0", withChecksums(map[string][]byte{testutil.AssetName("testext", "1.0.0") + ".tar.gz": archive}))
		inst, m := newInstaller(t, rs)
		if _, err := inst.Install(context.Background(), InstallRequest{Source: "acme/tools"}); !errors.Is(err, ErrBadArchive) {
			t.Errorf("%s: %v, want ErrBadArchive", name, err)
		}
		if len(m.List()) != 0 {
			t.Errorf("%s: installed anyway", name)
		}
		assertTmpEmpty(t, m)
	}
}

func TestInstallArchiveContentLimit(t *testing.T) {
	// a small download that unpacks to more than MaxDownload
	bomb := tarGz(t, tarEntry{name: "edelweiss-ext-testext", kind: tar.TypeReg, mode: 0o755, data: make([]byte, 1<<20)})
	rs := testutil.NewReleaseServer(t)
	rs.Add("acme/tools", "v1.0.0", withChecksums(map[string][]byte{testutil.AssetName("testext", "1.0.0") + ".tar.gz": bomb}))
	inst, m := newInstaller(t, rs)
	inst.opts.MaxDownload = 64 << 10
	if _, err := inst.Install(context.Background(), InstallRequest{Source: "acme/tools"}); !errors.Is(err, github.ErrTooLarge) {
		t.Fatalf("got %v", err)
	}
	if len(m.List()) != 0 {
		t.Fatal("installed anyway")
	}
	assertTmpEmpty(t, m)
}

// TestArchiveSizeOverflow: summing the announced sizes of the entries must not wrap around. A one byte
// README followed by an executable whose PAX header announces MaxInt64 bytes made the sum negative.
func TestArchiveSizeOverflow(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "README", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "backend", Typeflag: tar.TypeReg, Mode: 0o755, Size: math.MaxInt64}); err != nil {
		t.Fatal(err)
	}
	// a few MiB of the announced exabytes; the tar writer is not closed, it would complain about the missing rest
	if _, err := tw.Write(make([]byte, 4<<20)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	dir := testutil.TempDir(t)
	archive, dst := filepath.Join(dir, "asset"), filepath.Join(dir, "backend")
	if err := os.WriteFile(archive, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := extractExecutable(archive, dst, 64<<10); !errors.Is(err, github.ErrTooLarge) {
		t.Fatalf("got %v, want ErrTooLarge", err)
	}
	if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("something was extracted: %v", err)
	}
}

func TestInstallVerifies(t *testing.T) {
	bin := readBin(t, "")
	asset := testutil.AssetName("testext", "1.0.0")
	ctx := context.Background()

	t.Run("checksum mismatch", func(t *testing.T) {
		rs := testutil.NewReleaseServer(t)
		rs.Add("acme/tools", "v1", map[string][]byte{asset: bin, "checksums.txt": testutil.Checksums(map[string][]byte{asset: []byte("something else")})})
		inst, m := newInstaller(t, rs)
		logs := captureLog(inst)
		if _, err := inst.Install(ctx, InstallRequest{Source: "acme/tools"}); !errors.Is(err, ErrChecksumMismatch) {
			t.Fatalf("got %v", err)
		}
		if len(m.List()) != 0 {
			t.Fatal("installed despite the mismatch")
		}
		assertTmpEmpty(t, m)
		for _, want := range []string{"level=WARN", "repo=acme/tools", "tag=v1", "asset=" + asset,
			"listed=" + testutil.SHA256([]byte("something else")), "downloaded=" + testutil.SHA256(bin)} {
			if !strings.Contains(logs.String(), want) {
				t.Errorf("the log lacks %q:\n%s", want, logs)
			}
		}
	})
	t.Run("release changed after review", func(t *testing.T) {
		rs := testutil.NewReleaseServer(t)
		rs.Add("acme/tools", "v1", withChecksums(map[string][]byte{asset: bin}))
		inst, m := newInstaller(t, rs)
		logs := captureLog(inst)
		reviewed := testutil.SHA256([]byte("reviewed"))
		if _, err := inst.Install(ctx, InstallRequest{Source: "acme/tools", SHA256: reviewed}); !errors.Is(err, ErrReleaseChanged) {
			t.Fatalf("got %v", err)
		}
		if len(m.List()) != 0 {
			t.Fatal("installed despite the changed release")
		}
		assertTmpEmpty(t, m)
		for _, want := range []string{"level=WARN", "repo=acme/tools", "tag=v1", "asset=" + asset,
			"confirmed=" + reviewed, "listed=" + testutil.SHA256(bin)} {
			if !strings.Contains(logs.String(), want) {
				t.Errorf("the log lacks %q:\n%s", want, logs)
			}
		}
	})
	t.Run("version differs from the asset name", func(t *testing.T) {
		rs := testutil.NewReleaseServer(t)
		rs.Add("acme/tools", "v2", withChecksums(map[string][]byte{testutil.AssetName("testext", "2.0.0"): bin}))
		inst, m := newInstaller(t, rs)
		if _, err := inst.Install(ctx, InstallRequest{Source: "acme/tools"}); !errors.Is(err, ErrInvalidExtension) {
			t.Fatalf("got %v", err)
		}
		if len(m.List()) != 0 {
			t.Fatal("installed despite the version mismatch")
		}
	})
	t.Run("id differs from the asset name", func(t *testing.T) {
		rs := testutil.NewReleaseServer(t)
		rs.Add("acme/tools", "v1", withChecksums(map[string][]byte{testutil.AssetName("other", "1.0.0"): bin}))
		inst, m := newInstaller(t, rs)
		if _, err := inst.Install(ctx, InstallRequest{Source: "acme/tools"}); !errors.Is(err, ErrInvalidExtension) {
			t.Fatalf("got %v", err)
		}
		if len(m.List()) != 0 {
			t.Fatal("installed despite the id mismatch")
		}
		assertTmpEmpty(t, m)
	})
	t.Run("several extensions in one release", func(t *testing.T) {
		rs := testutil.NewReleaseServer(t)
		rs.Add("acme/tools", "v1", withChecksums(map[string][]byte{asset: bin, testutil.AssetName("zeta", "1.0.0"): []byte("never downloaded")}))
		inst, m := newInstaller(t, rs)
		if _, err := inst.Install(ctx, InstallRequest{Source: "acme/tools"}); !errors.Is(err, ErrChooseID) {
			t.Fatalf("without id: %v", err)
		}
		if _, err := inst.Install(ctx, InstallRequest{Source: "acme/tools", ID: "nope"}); !errors.Is(err, ErrUnknownCandidate) {
			t.Fatalf("unknown id: %v", err)
		}
		if _, err := inst.Install(ctx, InstallRequest{Source: "acme/tools", ID: "testext"}); err != nil {
			t.Fatalf("with id: %v", err)
		}
		if _, ok := m.Get("testext"); !ok {
			t.Fatal("not installed")
		}
	})
	t.Run("download over the size limit", func(t *testing.T) {
		rs := testutil.NewReleaseServer(t)
		rs.Add("acme/tools", "v1", withChecksums(map[string][]byte{asset: bin}))
		inst, m := newInstaller(t, rs)
		inst.opts.MaxDownload = int64(len(bin)) - 1
		if _, err := inst.Install(ctx, InstallRequest{Source: "acme/tools"}); !errors.Is(err, github.ErrTooLarge) {
			t.Fatalf("got %v", err)
		}
		assertTmpEmpty(t, m)
	})
}

func TestInstallIsExclusive(t *testing.T) {
	rs := testutil.NewReleaseServer(t)
	bin := readBin(t, "")
	rs.Add("acme/tools", "v1.0.0", withChecksums(map[string][]byte{testutil.AssetName("testext", "1.0.0"): bin}))
	inst, _ := newInstaller(t, rs)
	release, started := rs.Gate()
	defer release()

	done := make(chan error, 1)
	go func() {
		_, err := inst.Install(context.Background(), InstallRequest{Source: "acme/tools"})
		done <- err
	}()
	select {
	case <-started: // the first install is downloading
	case err := <-done:
		t.Fatalf("the first install ended before it downloaded: %v", err)
	}
	if _, err := inst.Install(context.Background(), InstallRequest{Source: "acme/tools"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("second install: %v", err)
	}
	if _, err := inst.Update(context.Background(), "testext", ""); !errors.Is(err, ErrBusy) {
		t.Fatalf("update during install: %v", err)
	}
	release()
	if err := <-done; err != nil {
		t.Fatalf("first install: %v", err)
	}
}

func TestUpdates(t *testing.T) {
	rs := testutil.NewReleaseServer(t)
	old, newer := readBin(t, ""), readBin(t, "1.1.0")
	rs.Add("acme/tools", "v1.0.0", withChecksums(map[string][]byte{testutil.AssetName("testext", "1.0.0"): old}))
	inst, m := newInstaller(t, rs)
	ctx := context.Background()
	if _, err := inst.Install(ctx, InstallRequest{Source: "acme/tools@v1.0.0"}); err != nil {
		t.Fatal(err)
	}
	if updates, failed := inst.Updates(ctx); len(updates) != 0 || len(failed) != 0 {
		t.Fatalf("up to date: %v %v", updates, failed)
	}
	if _, err := inst.Update(ctx, "testext", ""); !errors.Is(err, ErrUpToDate) {
		t.Fatalf("update when up to date: %v", err)
	}

	asset := testutil.AssetName("testext", "1.1.0")
	rs.Add("acme/tools", "v1.1.0", withChecksums(map[string][]byte{asset: newer}))
	updates, failed := inst.Updates(ctx)
	want := Update{ID: "testext", Installed: "1.0.0", Available: "1.1.0", Repo: "acme/tools", Tag: "v1.1.0", Asset: asset, SHA256: testutil.SHA256(newer)}
	if len(failed) != 0 || !slices.Equal(updates, []Update{want}) {
		t.Fatalf("updates %+v, failed %+v", updates, failed)
	}
	// the checksum of the update list is the confirmation (upper case digits are the same checksum)
	ext, err := inst.Update(ctx, "testext", strings.ToUpper(want.SHA256))
	if err != nil {
		t.Fatal(err)
	}
	if ext.Manifest.Version != "1.1.0" || ext.Source.Tag != "v1.1.0" {
		t.Fatalf("after update: %s %+v", ext.Manifest.Version, ext.Source)
	}
	if got, _ := m.Get("testext"); got.Manifest.Version != "1.1.0" {
		t.Fatalf("store still has %s", got.Manifest.Version)
	}

	if _, err := inst.Update(ctx, "nope", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown extension: %v", err)
	}
}

// TestUpdateHonoursConfirmedChecksum: what the user confirmed in the update list is what Update installs, or nothing.
func TestUpdateHonoursConfirmedChecksum(t *testing.T) {
	old, newer := readBin(t, ""), readBin(t, "1.1.0")
	asset := testutil.AssetName("testext", "1.1.0")
	for name, change := range map[string]func(*testutil.ReleaseServer){
		"a newer release was published": func(rs *testutil.ReleaseServer) {
			rs.Add("acme/tools", "v1.2.0", withChecksums(map[string][]byte{testutil.AssetName("testext", "1.2.0"): []byte("newest")}))
		},
		"the release was replaced": func(rs *testutil.ReleaseServer) {
			rs.Add("acme/tools", "v1.1.0", withChecksums(map[string][]byte{asset: []byte("replaced")}))
		},
	} {
		t.Run(name, func(t *testing.T) {
			rs := testutil.NewReleaseServer(t)
			rs.Add("acme/tools", "v1.0.0", withChecksums(map[string][]byte{testutil.AssetName("testext", "1.0.0"): old}))
			inst, m := newInstaller(t, rs)
			ctx := context.Background()
			if _, err := inst.Install(ctx, InstallRequest{Source: "acme/tools"}); err != nil {
				t.Fatal(err)
			}
			rs.Add("acme/tools", "v1.1.0", withChecksums(map[string][]byte{asset: newer}))
			updates, failed := inst.Updates(ctx)
			if len(failed) != 0 || len(updates) != 1 || updates[0].SHA256 != testutil.SHA256(newer) {
				t.Fatalf("updates %+v, failed %+v", updates, failed)
			}

			change(rs)
			logs := captureLog(inst)
			if _, err := inst.Update(ctx, "testext", updates[0].SHA256); !errors.Is(err, ErrReleaseChanged) {
				t.Fatalf("got %v", err)
			}
			if got, _ := m.Get("testext"); got.Manifest.Version != "1.0.0" || got.Source.Tag != "v1.0.0" {
				t.Fatalf("the store changed: %s %+v", got.Manifest.Version, got.Source)
			}
			assertTmpEmpty(t, m)
			if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "confirmed="+updates[0].SHA256) {
				t.Errorf("the log lacks the warning:\n%s", logs)
			}
		})
	}
}

func TestUpdatesSkipsAndReports(t *testing.T) {
	rs := testutil.NewReleaseServer(t)
	inst, m := newInstaller(t, rs)
	ctx := context.Background()
	// installed from a file: not checked, and Update refuses it
	if _, err := m.InstallFile(ctx, testExt(t), Source{Type: "file", Location: "/tmp/testext"}, ""); err != nil {
		t.Fatal(err)
	}
	if updates, failed := inst.Updates(ctx); len(updates) != 0 || len(failed) != 0 {
		t.Fatalf("file install checked: %v %v", updates, failed)
	}
	if _, err := inst.Update(ctx, "testext", ""); !errors.Is(err, ErrNotFromGitHub) {
		t.Fatalf("update of a file install: %v", err)
	}
	// from a repository that no longer has releases: reported, not fatal
	if _, err := m.InstallFile(ctx, testExt(t), Source{Type: "github", Location: "github.com/acme/gone", Repo: "acme/gone"}, ""); err != nil {
		t.Fatal(err)
	}
	updates, failed := inst.Updates(ctx)
	if len(updates) != 0 || len(failed) != 1 || failed[0].ID != "testext" || !errors.Is(failed[0].Err, github.ErrReleaseNotFound) {
		t.Fatalf("updates %v, failed %v", updates, failed)
	}
}

func TestResolveWithoutManager(t *testing.T) {
	rs := testutil.NewReleaseServer(t)
	rs.Add("acme/tools", "v1", withChecksums(map[string][]byte{testutil.AssetName("testext", "1.0.0"): []byte("x")}))
	inst := NewInstaller(InstallerOptions{Releases: github.NewClient(rs.URL, "", "test"), Arch: runtime.GOARCH, MaxDownload: 1 << 20})
	if res, err := inst.Resolve(context.Background(), "acme/tools"); err != nil || len(res.Candidates) != 1 {
		t.Fatalf("resolve without a store: %+v %v", res, err)
	}
	if _, err := Pick(&Resolution{Candidates: []Candidate{{ID: "a"}, {ID: "b"}}}, ""); !errors.Is(err, ErrChooseID) {
		t.Fatal("Pick without id")
	}
}
