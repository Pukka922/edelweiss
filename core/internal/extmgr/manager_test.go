package extmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Pukka922/edelweiss/core/internal/config"
	"github.com/Pukka922/edelweiss/core/internal/testutil"
)

type changeLog struct {
	mu  sync.Mutex
	ids []string
}

func (c *changeLog) add(id string) { c.mu.Lock(); c.ids = append(c.ids, id); c.mu.Unlock() }
func (c *changeLog) list() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.ids)
}

func newManager(t *testing.T, state string, dev []config.DevExtension) (*Manager, *changeLog) {
	t.Helper()
	changes := &changeLog{}
	m, err := New(Options{
		Dir:      filepath.Join(state, "extensions"),
		TmpDir:   filepath.Join(state, "tmp"),
		Querier:  newQuerier(t),
		Dev:      dev,
		OnChange: changes.add,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m, changes
}

func testExt(t *testing.T) string {
	t.Helper()
	bin, err := testutil.BuildTestExt()
	if err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestInstallFileListGetRemove(t *testing.T) {
	state := testutil.TempDir(t)
	m, changes := newManager(t, state, nil)
	ctx := context.Background()

	ext, err := m.InstallFile(ctx, testExt(t), Source{Type: "file", Location: "/tmp/testext"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if ext.Manifest.ID != "testext" || len(ext.Source.SHA256) != 64 || ext.Source.InstalledAt.IsZero() {
		t.Fatalf("unexpected extension %+v", ext)
	}
	fi, err := os.Stat(filepath.Join(state, "extensions", "testext", "backend"))
	if err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("backend: %v %v", fi, err)
	}
	var stored map[string]any
	data, _ := os.ReadFile(filepath.Join(state, "extensions", "testext", "manifest.json"))
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("stored manifest %s: %v", data, err)
	}
	if icon, _ := stored["iconSvg"].(string); icon == "" {
		t.Fatalf("stored manifest has no iconSvg: %s", data)
	}
	if got := m.List(); len(got) != 1 || got[0].Manifest.ID != "testext" {
		t.Fatalf("List = %v", got)
	}
	if _, ok := m.Get("testext"); !ok {
		t.Fatal("Get failed")
	}
	if !slices.Equal(changes.list(), []string{"testext"}) {
		t.Fatalf("changes %v", changes.list())
	}

	// a fresh manager on the same directory finds the installed extension
	m2, _ := newManager(t, state, nil)
	if err := m2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := m2.Get("testext"); !ok {
		t.Fatal("Load did not find installed extension")
	}

	if err := m.Remove("testext"); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Get("testext"); ok {
		t.Fatal("still present after Remove")
	}
	if _, err := os.Stat(filepath.Join(state, "extensions", "testext")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("directory still exists")
	}
	if err := m.Remove("testext"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Remove: %v", err)
	}
}

func TestInstallExpectIDMismatch(t *testing.T) {
	m, _ := newManager(t, testutil.TempDir(t), nil)
	if _, err := m.InstallFile(context.Background(), testExt(t), Source{Type: "file"}, "docker"); err == nil {
		t.Fatal("expected id mismatch error")
	}
	if len(m.List()) != 0 {
		t.Fatal("extension installed despite mismatch")
	}
}

// A canceled or timed out context is reported as such, not as an invalid extension.
func TestInstallContextDone(t *testing.T) {
	m, _ := newManager(t, testutil.TempDir(t), nil)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	for want, ctx := range map[error]context.Context{context.Canceled: canceled, context.DeadlineExceeded: expired} {
		_, err := m.Install(ctx, testExt(t), Source{Type: "file"}, Expect{})
		if err != want { // the context's error itself, unwrapped
			t.Errorf("got %v, want exactly %v", err, want)
		}
		if errors.Is(err, ErrInvalidExtension) {
			t.Errorf("%v is reported as an invalid extension", err)
		}
	}
	if len(m.List()) != 0 {
		t.Fatal("installed with a done context")
	}
}

// "shell" and "sdk" are the i18n namespaces of the shell and the SDK: the store refuses extensions with such an id.
func TestInstallRejectsReservedID(t *testing.T) {
	for _, id := range []string{"shell", "sdk"} {
		state := testutil.TempDir(t)
		m, _ := newManager(t, state, nil)
		p := filepath.Join(state, "ext-"+id)
		script := "#!/bin/sh\n" +
			`echo '{"id":"` + id + `","version":"1.0.0","apiVersion":1,"name":{"en":"X"},"menu":{"section":"tools"}}'` + "\n"
		if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := m.InstallFile(context.Background(), p, Source{Type: "file"}, "")
		if err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("InstallFile with id %q: err = %v, want a reserved-id error", id, err)
		}
		if len(m.List()) != 0 {
			t.Fatalf("extension %q installed despite its reserved id", id)
		}
		if _, err := os.Stat(filepath.Join(state, "extensions", id)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("directory for %q exists: %v", id, err)
		}
	}
}

func TestInstallRejectsGarbage(t *testing.T) {
	state := testutil.TempDir(t)
	m, _ := newManager(t, state, nil)
	p := filepath.Join(state, "garbage")
	_ = os.WriteFile(p, []byte("nope"), 0o644)
	if _, err := m.InstallFile(context.Background(), p, Source{Type: "file"}, ""); !errors.Is(err, ErrWrongArch) {
		t.Fatalf("err = %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(state, "tmp"))
	if len(entries) != 0 {
		t.Fatalf("staging not cleaned up: %v", entries)
	}
}

func TestReinstallReplaces(t *testing.T) {
	m, changes := newManager(t, testutil.TempDir(t), nil)
	ctx := context.Background()
	if _, err := m.InstallFile(ctx, testExt(t), Source{Type: "file", Location: "a"}, ""); err != nil {
		t.Fatal(err)
	}
	ext, err := m.InstallFile(ctx, testExt(t), Source{Type: "file", Location: "b"}, "testext")
	if err != nil {
		t.Fatal(err)
	}
	if ext.Source.Location != "b" || len(changes.list()) != 2 {
		t.Fatalf("reinstall: %+v %v", ext.Source, changes.list())
	}
}

func TestDevExtension(t *testing.T) {
	bin := testExt(t)
	m, _ := newManager(t, testutil.TempDir(t), []config.DevExtension{{Binary: bin, UIDir: "/work/ui"}})
	if err := m.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	ext, ok := m.Get("testext")
	if !ok || !ext.Dev || ext.UIDir != "/work/ui" || ext.Binary != bin {
		t.Fatalf("dev extension %+v", ext)
	}
}

func TestWatchPicksUpExternalInstall(t *testing.T) {
	state := testutil.TempDir(t)
	daemon, changes := newManager(t, state, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := daemon.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if err := daemon.Watch(ctx); err != nil {
		t.Fatal(err)
	}
	cli, _ := newManager(t, state, nil) // simulates `edelweissd ext install`
	if _, err := cli.InstallFile(ctx, testExt(t), Source{Type: "file"}, ""); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := daemon.Get("testext"); ok && slices.Contains(changes.list(), "testext") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("watcher did not pick up the installation")
}

// Every change to the store reaches the daemon's log once: its own installs
// and removals, and the ones another process (`edelweissd ext …`) made, which
// it picks up in Load.
func TestStoreChangesAreLogged(t *testing.T) {
	state := testutil.TempDir(t)
	logs := &syncLog{}
	daemon, err := New(Options{
		Dir: filepath.Join(state, "extensions"), TmpDir: filepath.Join(state, "tmp"), Querier: newQuerier(t),
		Logger: slog.New(slog.NewTextHandler(logs, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	load := func() string {
		t.Helper()
		before := len(logs.String())
		if err := daemon.Load(ctx); err != nil {
			t.Fatal(err)
		}
		return logs.String()[before:]
	}
	if got := load(); got != "" {
		t.Fatalf("empty store logged %q", got)
	}

	cli, _ := newManager(t, state, nil) // another process working on the same store
	if _, err := cli.InstallFile(ctx, testExt(t), Source{Type: "file", Location: "a"}, ""); err != nil {
		t.Fatal(err)
	}
	if got := load(); !strings.Contains(got, `level=INFO msg="extension store: added" id=testext version=1.0.0`) {
		t.Fatalf("external install: %q", got)
	}
	if got := load(); got != "" {
		t.Fatalf("unchanged store logged %q", got)
	}
	// a rebuilt binary: same id and version, other contents
	data, err := os.ReadFile(testExt(t))
	if err != nil {
		t.Fatal(err)
	}
	rebuilt := filepath.Join(state, "rebuilt")
	if err := os.WriteFile(rebuilt, append(data, 0), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.InstallFile(ctx, rebuilt, Source{Type: "file", Location: "b"}, ""); err != nil {
		t.Fatal(err)
	}
	if got := load(); !strings.Contains(got, `level=INFO msg="extension store: updated" id=testext version=1.0.0`) {
		t.Fatalf("external update: %q", got)
	}
	if err := cli.Remove("testext"); err != nil {
		t.Fatal(err)
	}
	if got := load(); !strings.Contains(got, `level=INFO msg="extension store: removed" id=testext`) {
		t.Fatalf("external removal: %q", got)
	}

	// the daemon's own operations are logged by the operation, not again by Load
	before := len(logs.String())
	if _, err := daemon.InstallFile(ctx, testExt(t), Source{Type: "file"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := daemon.Remove("testext"); err != nil {
		t.Fatal(err)
	}
	load()
	own := logs.String()[before:]
	if strings.Count(own, `msg="extension store: installed" id=testext`) != 1 ||
		strings.Count(own, `msg="extension store: removed" id=testext`) != 1 || strings.Count(own, "\n") != 2 {
		t.Fatalf("own install and removal:\n%s", own)
	}
}

// syncLog is a goroutine-safe log sink.
type syncLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncLog) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncLog) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestNewSweepsOnlyStaleLeftovers(t *testing.T) {
	state := testutil.TempDir(t)
	extDir := filepath.Join(state, "extensions")
	tmpDir := filepath.Join(state, "tmp")
	staleOld := filepath.Join(extDir, ".old-testext-123")
	freshOld := filepath.Join(extDir, ".old-testext-456")
	staleInstall := filepath.Join(tmpDir, "install-abc")
	freshInstall := filepath.Join(tmpDir, "install-def") // e.g. an install running in another process
	keepExt := filepath.Join(extDir, "keepme")
	keepTmp := filepath.Join(tmpDir, "other")
	for _, d := range []string{staleOld, freshOld, staleInstall, freshInstall, keepExt, keepTmp} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{staleOld, freshOld, staleInstall, freshInstall} {
		if err := os.WriteFile(filepath.Join(d, "backend"), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// make everything but the fresh entries older than leftoverMaxAge (set
	// after the writes above, which touch the directories' mtime)
	past := time.Now().Add(-2 * leftoverMaxAge)
	for _, d := range []string{staleOld, staleInstall, keepExt, keepTmp} {
		if err := os.Chtimes(d, past, past); err != nil {
			t.Fatal(err)
		}
	}
	exists := func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	}

	var logs bytes.Buffer
	opts := Options{
		Dir:     extDir,
		TmpDir:  tmpDir,
		Querier: newQuerier(t),
		Logger:  slog.New(slog.NewTextHandler(&logs, nil)),
	}
	if _, err := New(opts); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{staleOld, staleInstall} {
		if exists(gone) {
			t.Errorf("stale %s still exists", gone)
		}
	}
	for _, kept := range []string{freshOld, freshInstall, keepExt, keepTmp} {
		if !exists(kept) {
			t.Errorf("%s was removed", kept)
		}
	}
	for _, want := range []string{".old-testext-123", "install-abc", "level=INFO"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log does not mention %q:\n%s", want, logs.String())
		}
	}
	for _, unwanted := range []string{".old-testext-456", "install-def"} {
		if strings.Contains(logs.String(), unwanted) {
			t.Errorf("log mentions fresh %q:\n%s", unwanted, logs.String())
		}
	}

	// "now" comes from Options.Now: later, the formerly fresh leftovers are stale too
	opts.Now = func() time.Time { return time.Now().Add(2 * leftoverMaxAge) }
	if _, err := New(opts); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{freshOld, freshInstall} {
		if exists(gone) {
			t.Errorf("%s still exists once it is older than leftoverMaxAge", gone)
		}
	}
	for _, kept := range []string{keepExt, keepTmp} {
		if !exists(kept) {
			t.Errorf("%s was removed", kept)
		}
	}
}

// replaceFixture creates <Dir>/x holding "old" and a staging dir holding "new".
func replaceFixture(t *testing.T, m *Manager, name string) (src, target string) {
	t.Helper()
	target = filepath.Join(m.opts.Dir, "x")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "v"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	return newStaging(t, m, name, "new"), target
}

func newStaging(t *testing.T, m *Manager, name, content string) string {
	t.Helper()
	src := filepath.Join(m.opts.TmpDir, name)
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "v"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return src
}

func assertReplaced(t *testing.T, m *Manager, src, target string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(target, "v"))
	if err != nil || string(got) != "new" {
		t.Fatalf("target content = %q, %v; want new", got, err)
	}
	if _, err := os.Stat(src); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("src still exists (err %v)", err)
	}
	entries, err := os.ReadDir(m.opts.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".old-") {
			t.Fatalf("leftover %s", e.Name())
		}
	}
}

func TestReplaceDirExisting(t *testing.T) {
	m, _ := newManager(t, testutil.TempDir(t), nil)
	src, target := replaceFixture(t, m, "install-1")
	if err := m.replaceDir(src, target); err != nil {
		t.Fatal(err)
	}
	assertReplaced(t, m, src, target)
}

func TestReplaceDirNewTarget(t *testing.T) {
	m, _ := newManager(t, testutil.TempDir(t), nil)
	src := newStaging(t, m, "install-1", "new")
	target := filepath.Join(m.opts.Dir, "x")
	if err := m.replaceDir(src, target); err != nil {
		t.Fatal(err)
	}
	assertReplaced(t, m, src, target)
}

func TestReplaceDirFallsBackWithoutExchange(t *testing.T) {
	for _, errno := range []error{syscall.EINVAL, syscall.ENOSYS} {
		t.Run(errno.Error(), func(t *testing.T) {
			orig := exchangeDirs
			t.Cleanup(func() { exchangeDirs = orig })
			exchangeDirs = func(a, b string) error { return errno }
			m, _ := newManager(t, testutil.TempDir(t), nil)
			src, target := replaceFixture(t, m, "install-1")
			if err := m.replaceDir(src, target); err != nil {
				t.Fatal(err)
			}
			assertReplaced(t, m, src, target)
		})
	}
}

func TestReplaceDirExchangeErrorKeepsTarget(t *testing.T) {
	orig := exchangeDirs
	t.Cleanup(func() { exchangeDirs = orig })
	exchangeDirs = func(a, b string) error { return syscall.EACCES }
	m, _ := newManager(t, testutil.TempDir(t), nil)
	src, target := replaceFixture(t, m, "install-1")
	if err := m.replaceDir(src, target); !errors.Is(err, syscall.EACCES) {
		t.Fatalf("err = %v, want EACCES", err)
	}
	if got, _ := os.ReadFile(filepath.Join(target, "v")); string(got) != "old" {
		t.Fatalf("target content = %q, want old", got)
	}
}

// With RENAME_EXCHANGE the extension directory is never absent while it is
// being replaced, so a concurrent reader (e.g. the watcher's Load) cannot
// observe a missing extension.
func TestReplaceDirNeverMissing(t *testing.T) {
	m, _ := newManager(t, testutil.TempDir(t), nil)
	src, target := replaceFixture(t, m, "probe-a")
	if err := exchangeDirs(src, target); err != nil {
		t.Skipf("RENAME_EXCHANGE unsupported here: %v", err)
	}
	var missing atomic.Int64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := os.Stat(filepath.Join(target, "v")); err != nil {
				missing.Add(1)
			}
		}
	}()
	for i := 0; i < 500; i++ {
		s := newStaging(t, m, "install-loop", "new")
		if err := m.replaceDir(s, target); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	<-done
	if n := missing.Load(); n != 0 {
		t.Fatalf("extension directory was missing %d times during replacement", n)
	}
}

func TestRemoveDeletesTheDataDirectory(t *testing.T) {
	state := testutil.TempDir(t)
	root := filepath.Join(state, "varlib")
	m, err := New(Options{Dir: filepath.Join(state, "extensions"), TmpDir: filepath.Join(state, "tmp"), Querier: newQuerier(t),
		ExtDataRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if got := m.ExtDataDir("testext"); got != filepath.Join(root, "edelweiss-ext-testext") {
		t.Fatalf("ExtDataDir = %s", got)
	}
	if got := m.ExtDataDir("../x"); got != "" {
		t.Fatalf("ExtDataDir of an invalid id = %q", got)
	}
	data := m.ExtDataDir("testext")
	other := filepath.Join(root, "edelweiss-ext-other")
	for _, d := range []string{data, other} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "registries.db"), []byte("x"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	install := func() {
		t.Helper()
		if _, err := m.InstallFile(context.Background(), testExt(t), Source{Type: "file", Location: "testext"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	install()
	install() // a reinstallation (an update) keeps the data
	if _, err := os.Stat(filepath.Join(data, "registries.db")); err != nil {
		t.Fatalf("an update removed the data: %v", err)
	}
	if err := m.Remove("testext"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(data); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the data directory is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(other, "registries.db")); err != nil {
		t.Fatalf("the data of another extension was touched: %v", err)
	}

	// a symlink is removed as a link; what it points to stays
	target := filepath.Join(state, "elsewhere")
	if err := os.MkdirAll(target, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "keep"), []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, data); err != nil {
		t.Fatal(err)
	}
	install()
	if err := m.Remove("testext"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(data); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the symlink is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "keep")); err != nil {
		t.Fatalf("the target of the symlink was removed: %v", err)
	}
}

func TestRemoveWithoutDataRoot(t *testing.T) {
	m, _ := newManager(t, testutil.TempDir(t), nil)
	if got := m.ExtDataDir("testext"); got != "" {
		t.Fatalf("ExtDataDir without a root = %q", got)
	}
}

// newDataManager is a manager whose data directories live below root.
func newDataManager(t *testing.T, state, root string) (*Manager, *changeLog) {
	t.Helper()
	changes := &changeLog{}
	m, err := New(Options{Dir: filepath.Join(state, "extensions"), TmpDir: filepath.Join(state, "tmp"), Querier: newQuerier(t),
		ExtDataRoot: root, OnChange: changes.add})
	if err != nil {
		t.Fatal(err)
	}
	return m, changes
}

func TestExtDataDirOnlyNamesTheConventionalDirectory(t *testing.T) {
	root := filepath.Join(testutil.TempDir(t), "varlib")
	m, _ := newDataManager(t, testutil.TempDir(t), root)
	for _, id := range []string{"", ".", "..", "../x", "a/b", "/etc", "x", "ab/", "testext/", "testext\n", "A-b", "shell", "sdk"} {
		if got := m.ExtDataDir(id); got != "" {
			t.Errorf("ExtDataDir(%q) = %q, want none", id, got)
		}
	}
	if got, want := m.ExtDataDir("my-ext1"), filepath.Join(root, "edelweiss-ext-my-ext1"); got != want {
		t.Errorf("ExtDataDir(my-ext1) = %q, want %q", got, want)
	}
	// a root that is not absolute would make Remove delete relative to the working directory
	for _, rel := range []string{".", "..", "varlib", "./varlib", "../varlib"} {
		if got := newRootManager(t, rel).ExtDataDir("testext"); got != "" {
			t.Errorf("ExtDataDir with the relative root %q = %q, want none", rel, got)
		}
	}
}

func newRootManager(t *testing.T, root string) *Manager {
	t.Helper()
	m, _ := newDataManager(t, testutil.TempDir(t), root)
	return m
}

func TestRemoveReportsADataDirectoryItCannotDelete(t *testing.T) {
	state := testutil.TempDir(t)
	// A root that is a regular file: nothing below it can be removed (ENOTDIR), as root as well as an ordinary user.
	root := filepath.Join(state, "not-a-directory")
	if err := os.WriteFile(root, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	m, changes := newDataManager(t, state, root)
	if _, err := m.InstallFile(context.Background(), testExt(t), Source{Type: "file", Location: "testext"}, ""); err != nil {
		t.Fatal(err)
	}

	err := m.Remove("testext")
	var dataErr *DataNotRemovedError
	if !errors.Is(err, ErrDataNotRemoved) || !errors.As(err, &dataErr) {
		t.Fatalf("Remove = %v, want a DataNotRemovedError", err)
	}
	if dataErr.Dir != filepath.Join(root, "edelweiss-ext-testext") || dataErr.Err == nil ||
		!strings.Contains(err.Error(), dataErr.Dir) || !strings.Contains(err.Error(), dataErr.Err.Error()) {
		t.Fatalf("the error does not name the directory and the cause: %+v (%v)", dataErr, err)
	}
	// The extension itself is gone and its processes were told to stop, so a retry would find nothing to remove.
	if _, ok := m.Get("testext"); ok {
		t.Fatal("the extension is still installed")
	}
	if _, err := os.Stat(filepath.Join(state, "extensions", "testext")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the extension directory is still there: %v", err)
	}
	if got := changes.list(); !slices.Equal(got, []string{"testext", "testext"}) { // installed, removed
		t.Fatalf("changes = %v", got)
	}
	if err := m.Remove("testext"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Remove = %v", err)
	}
}

func TestRemoveDoesNotFollowSymlinksInTheDataDirectory(t *testing.T) {
	state := testutil.TempDir(t)
	root := filepath.Join(state, "varlib")
	m, _ := newDataManager(t, state, root)
	data := m.ExtDataDir("testext")
	outside := filepath.Join(state, "outside")
	for _, d := range []string{data, outside, filepath.Join(outside, "sub")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	keep := filepath.Join(outside, "sub", "keep")
	file := filepath.Join(outside, "file")
	for _, f := range []string{keep, file} {
		if err := os.WriteFile(f, []byte("x"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{"dir": outside, "file": file, "dangling": filepath.Join(state, "missing")} {
		if err := os.Symlink(target, filepath.Join(data, link)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(data, "nested"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(data, "nested", "up")); err != nil {
		t.Fatal(err)
	}

	if _, err := m.InstallFile(context.Background(), testExt(t), Source{Type: "file", Location: "testext"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove("testext"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(data); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the data directory is still there: %v", err)
	}
	for _, f := range []string{keep, file} {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("%s, which a symlink in the data directory pointed to, was removed: %v", f, err)
		}
	}
}
