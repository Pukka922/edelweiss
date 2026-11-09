package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Pukka922/edelweiss/core/internal/github"
)

type fakeDownloads struct {
	content []byte
	err     error
	gotName string
	gotMax  int64
}

func (f *fakeDownloads) Release(context.Context, github.Source) (*github.Release, error) {
	return nil, errors.New("not used")
}

func (f *fakeDownloads) Download(_ context.Context, a github.Asset, w io.Writer, limit int64) (int64, error) {
	f.gotName, f.gotMax = a.Name, limit
	if f.err != nil {
		return 0, f.err
	}
	return io.Copy(w, bytes.NewReader(f.content))
}

// fakeEdelweissd is a shell script that acts as the new binary: it prints the version for "version" and, for
// "service script --binary <path>", what the given shell command prints ($4 is the path).
func fakeEdelweissd(version, script string) []byte {
	return []byte("#!/bin/sh\ncase \"$1\" in\nversion) echo '" + version + "' ;;\nservice) " + script + " ;;\nesac\n")
}

const goodScript = `printf '#!/sbin/openrc-run\ncommand="%s"\n' "$4"`

func offerFor(content []byte, latest string) Offer {
	sum := sha256.Sum256(content)
	return Offer{Latest: latest, Available: true, Asset: "edelweissd_" + latest + "_linux_amd64", Size: int64(len(content)),
		SHA256: hex.EncodeToString(sum[:]), asset: github.Asset{ID: 7, Name: "edelweissd_" + latest + "_linux_amd64"}}
}

func entries(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}

func TestPrepare(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "edelweissd")
	content := fakeEdelweissd("0.3.1", goodScript)
	rel := &fakeDownloads{content: content}
	o := offerFor(content, "0.3.1")
	st, err := Prepare(context.Background(), rel, o, o.SHA256, binary, 1<<20, ExecRun)
	if err != nil {
		t.Fatal(err)
	}
	want := binary + ".edelweiss-new"
	if st.Path != want || st.Version != "0.3.1" {
		t.Errorf("Staged = %+v, want path %s and version 0.3.1", st, want)
	}
	if !strings.HasPrefix(st.Script, "#!/sbin/openrc-run") || !strings.Contains(st.Script, `command="`+binary+`"`) {
		t.Errorf("Script = %q", st.Script)
	}
	if got := entries(t, dir); len(got) != 1 || got[0] != "edelweissd.edelweiss-new" {
		t.Errorf("files = %v, want only the staged binary", got)
	}
	if fi, err := os.Stat(want); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("staged binary: %v, %v", fi, err)
	}
	if rel.gotName != o.Asset || rel.gotMax != 1<<20 {
		t.Errorf("downloaded %q with limit %d", rel.gotName, rel.gotMax)
	}
}

func prepareFails(t *testing.T, content []byte, mutate func(o *Offer, confirmed *string), want error) {
	t.Helper()
	dir := t.TempDir()
	o := offerFor(content, "0.3.1")
	confirmed := o.SHA256
	if mutate != nil {
		mutate(&o, &confirmed)
	}
	_, err := Prepare(context.Background(), &fakeDownloads{content: content}, o, confirmed, filepath.Join(dir, "edelweissd"), 1<<20, ExecRun)
	if !errors.Is(err, want) {
		t.Errorf("Prepare = %v, want %v", err, want)
	}
	if got := entries(t, dir); len(got) != 0 {
		t.Errorf("left behind: %v", got)
	}
}

func TestPrepareChecksumMismatchLeavesNothing(t *testing.T) {
	prepareFails(t, fakeEdelweissd("0.3.1", goodScript), func(o *Offer, confirmed *string) {
		o.SHA256 = strings.Repeat("0", 64)
		*confirmed = o.SHA256
	}, ErrChecksumMismatch)
}

func TestPrepareWrongVersion(t *testing.T) {
	prepareFails(t, fakeEdelweissd("0.3.2", goodScript), nil, ErrWrongVersion)
}

func TestPrepareNotRunnable(t *testing.T) {
	prepareFails(t, []byte("\x00\x01 not an executable"), nil, ErrNotRunnable)
}

func TestPrepareBadScript(t *testing.T) {
	prepareFails(t, fakeEdelweissd("0.3.1", `echo hello`), nil, ErrBadScript)
	prepareFails(t, fakeEdelweissd("0.3.1", `printf '#!/sbin/openrc-run\ncommand="/elsewhere"\n'`), nil, ErrBadScript)
}

func TestPrepareReleaseChanged(t *testing.T) {
	prepareFails(t, fakeEdelweissd("0.3.1", goodScript), func(_ *Offer, confirmed *string) {
		*confirmed = strings.Repeat("1", 64)
	}, ErrReleaseChanged)
}

// The space needed: the staged binary, the helper's backup of the running one and the temp copy of a rollback, three
// times the size, while the old inode is still held, and 16 MiB.
func TestPrepareNoSpace(t *testing.T) {
	var asked string
	old := freeSpace
	t.Cleanup(func() { freeSpace = old })
	content := fakeEdelweissd("0.3.1", goodScript)
	o := offerFor(content, "0.3.1")
	freeSpace = func(dir string) (uint64, error) { asked = dir; return 3*uint64(o.Size) + 16<<20 - 1, nil }
	dir := t.TempDir()
	_, err := Prepare(context.Background(), &fakeDownloads{content: content}, o, o.SHA256, filepath.Join(dir, "edelweissd"), 1<<20, ExecRun)
	if !errors.Is(err, ErrNoSpace) || asked != dir {
		t.Errorf("Prepare = %v (asked %q), want ErrNoSpace for %s", err, asked, dir)
	}
	if got := entries(t, dir); len(got) != 0 {
		t.Errorf("left behind: %v", got)
	}
	freeSpace = func(string) (uint64, error) { return 3*uint64(o.Size) + 16<<20, nil }
	if _, err := Prepare(context.Background(), &fakeDownloads{content: content}, o, o.SHA256, filepath.Join(dir, "edelweissd"), 1<<20, ExecRun); err != nil {
		t.Errorf("Prepare with exactly enough space = %v", err)
	}
}

// What a crash of an earlier update left next to the binary goes before the download: only regular files with the
// names of this package's temp files.
func TestPrepareSweepsLeftovers(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "edelweissd")
	gone := []string{".edelweissd-dl-0123456789abcdef", ".edelweissd-123456", ".edelweissd.edelweiss-bak-42"}
	kept := []string{
		"edelweissd.edelweiss-bak", ".edelweissd-dl-short", ".edelweissd-12ab", ".edelweissd.edelweiss-bak-",
		".edelweissd-", ".other-1", "notes",
	}
	for _, name := range append(slices.Clone(gone), kept...) {
		writeFile(t, filepath.Join(dir, name), "left", 0o600)
	}
	if err := os.Mkdir(filepath.Join(dir, ".edelweissd-777"), 0o700); err != nil { // a directory stays
		t.Fatal(err)
	}
	if err := os.Symlink("notes", filepath.Join(dir, ".edelweissd-888")); err != nil { // and so does a link
		t.Fatal(err)
	}
	content := fakeEdelweissd("0.3.1", goodScript)
	o := offerFor(content, "0.3.1")
	if _, err := Prepare(context.Background(), &fakeDownloads{content: content}, o, o.SHA256, binary, 1<<20, ExecRun); err != nil {
		t.Fatal(err)
	}
	want := append(slices.Clone(kept), ".edelweissd-777", ".edelweissd-888", "edelweissd.edelweiss-new")
	slices.Sort(want)
	if got := entries(t, dir); !slices.Equal(got, want) {
		t.Errorf("files = %q, want %q", got, want)
	}
}

func TestPrepareDownloadErrorLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	content := fakeEdelweissd("0.3.1", goodScript)
	o := offerFor(content, "0.3.1")
	boom := errors.New("boom")
	_, err := Prepare(context.Background(), &fakeDownloads{content: content, err: boom}, o, o.SHA256, filepath.Join(dir, "edelweissd"), 1<<20, ExecRun)
	if !errors.Is(err, boom) {
		t.Errorf("Prepare = %v, want boom", err)
	}
	if got := entries(t, dir); len(got) != 0 {
		t.Errorf("left behind: %v", got)
	}
}

func TestPrepareCancelledContext(t *testing.T) {
	dir := t.TempDir()
	content := fakeEdelweissd("0.3.1", goodScript)
	o := offerFor(content, "0.3.1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Prepare(ctx, &fakeDownloads{content: content}, o, o.SHA256, filepath.Join(dir, "edelweissd"), 1<<20, ExecRun)
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrNotRunnable) || errors.Is(err, ErrBadScript) {
		t.Errorf("Prepare = %v, want the cancellation", err)
	}
	if got := entries(t, dir); len(got) != 0 {
		t.Errorf("left behind: %v", got)
	}
	// the same when the cancellation comes while the new binary runs
	ctx, cancel = context.WithCancel(context.Background())
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		cancel()
		return nil, errors.New("killed")
	}
	_, err = Prepare(ctx, &fakeDownloads{content: content}, o, o.SHA256, filepath.Join(dir, "edelweissd"), 1<<20, run)
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrNotRunnable) {
		t.Errorf("Prepare = %v, want the cancellation", err)
	}
}

func TestPrepareNeedsAnAbsoluteBinary(t *testing.T) {
	content := fakeEdelweissd("0.3.1", goodScript)
	o := offerFor(content, "0.3.1")
	if _, err := Prepare(context.Background(), &fakeDownloads{content: content}, o, o.SHA256, "edelweissd", 1<<20, ExecRun); err == nil {
		t.Error("Prepare accepted a relative binary")
	}
}
