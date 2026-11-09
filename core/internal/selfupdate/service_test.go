package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Pukka922/edelweiss/core/internal/github"
	"github.com/Pukka922/edelweiss/core/internal/oplock"
	"github.com/Pukka922/edelweiss/core/internal/service"
)

// svcContent is the binary the fake release offers.
var svcContent = []byte("the edelweissd of the release")

func svcDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// svcGitHub serves one release of edelweissd for amd64: the binary (asset 1) and checksums.txt (asset 2).
type svcGitHub struct {
	mu        sync.Mutex
	rel       *github.Release
	files     map[int64][]byte
	err       error    // of Release
	releases  int      // calls of Release
	downloads []string // the names of the assets downloaded
	// onBinary, when set, runs in place of the download of the binary.
	onBinary func(ctx context.Context) error
}

// publish makes version the latest release, with content as its binary and digest as the checksum it lists.
func (f *svcGitHub) publish(version string, content []byte, digest string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := "edelweissd_" + version + "_linux_amd64"
	f.rel = &github.Release{TagName: "v" + version, HTMLURL: "https://example.test/v" + version,
		PublishedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		Assets:      []github.Asset{{ID: 1, Name: name, Size: int64(len(content))}, {ID: 2, Name: "checksums.txt"}}}
	f.files = map[int64][]byte{1: content, 2: []byte(digest + "  " + name + "\n")}
}

func (f *svcGitHub) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *svcGitHub) Release(context.Context, github.Source) (*github.Release, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases++
	if f.err != nil {
		return nil, f.err
	}
	return f.rel, nil
}

func (f *svcGitHub) Download(ctx context.Context, a github.Asset, w io.Writer, limit int64) (int64, error) {
	f.mu.Lock()
	f.downloads = append(f.downloads, a.Name)
	data, ok := f.files[a.ID]
	onBinary := f.onBinary
	f.mu.Unlock()
	if onBinary != nil && a.ID == 1 {
		return 0, onBinary(ctx)
	}
	if !ok {
		return 0, fmt.Errorf("no asset %d", a.ID)
	}
	if int64(len(data)) > limit {
		return 0, github.ErrTooLarge
	}
	n, err := w.Write(data)
	return int64(n), err
}

func (f *svcGitHub) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.releases
}

// downloaded tells whether the binary of the release was downloaded.
func (f *svcGitHub) downloaded() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.ContainsFunc(f.downloads, func(n string) bool { return strings.HasPrefix(n, "edelweissd_") })
}

// svcLauncher stands in for ExecLauncher: like the helper it keeps the lock it is handed (a duplicate of the fd shares
// the flock), and done ends when the test says so.
type svcLauncher struct {
	mu      sync.Mutex
	err     error
	nilDone bool // Launch answers nil, nil
	calls   int
	plan    string
	held    *os.File
	done    chan error
}

func (l *svcLauncher) Launch(planPath string, lock *os.File) (<-chan error, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	if l.err != nil || l.nilDone {
		return nil, l.err
	}
	fd, err := syscall.Dup(int(lock.Fd()))
	if err != nil {
		return nil, err
	}
	l.plan, l.held, l.done = planPath, os.NewFile(uintptr(fd), "lock"), make(chan error, 1)
	return l.done, nil
}

// exit ends the helper: it gives up the lock, and done reports err.
func (l *svcLauncher) exit(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held != nil {
		_ = l.held.Close()
		l.held = nil
	}
	if l.done != nil {
		l.done <- err
		l.done = nil
	}
}

func (l *svcLauncher) launched() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

type svcClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *svcClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *svcClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// svcEnv is a host a self-update is possible on: a fixture root with OpenRC, the init script of the binary and a
// supervise-daemon parent, a binary in a directory of its own, a state directory, and fakes for GitHub, the new binary
// and the helper.
type svcEnv struct {
	opts     Options
	gh       *svcGitHub
	launcher *svcLauncher
	clock    *svcClock
	root     string
	binary   string
	dir      string // <state>/self-update

	mu         sync.Mutex
	newVersion string // what the downloaded binary says it is
	runs       []string
	onRun      func(args []string) // called before the new binary answers
}

func newSvcEnv(t *testing.T) *svcEnv {
	t.Helper()
	e := &svcEnv{root: t.TempDir(), newVersion: "0.3.1", gh: &svcGitHub{}, launcher: &svcLauncher{},
		clock: &svcClock{t: time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)}}
	binDir := filepath.Join(t.TempDir(), "bin")
	e.binary = filepath.Join(binDir, "edelweissd")
	writeFile(t, e.binary, "edelweissd 0.3.0", 0o755)
	script, err := service.Script(e.binary)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(e.root, "proc/mounts"), "/dev/sda2 / ext4 rw 0 0\n", 0o644)
	writeFile(t, filepath.Join(e.root, "usr/sbin/rc-service"), "", 0o755)
	writeFile(t, filepath.Join(e.root, "etc/init.d/edelweiss"), script, 0o755)
	writeFile(t, filepath.Join(e.root, "proc/42/comm"), "supervise-daemon\n", 0o644)
	state := t.TempDir()
	e.dir = filepath.Join(state, "self-update")
	e.gh.publish("0.3.1", svcContent, svcDigest(svcContent))
	e.opts = Options{
		Releases: e.gh, Source: Source, Arch: "amd64", Version: "0.3.0", Binary: e.binary, Root: e.root,
		StateDir: state, Listen: "0.0.0.0:9090", TLS: true, MaxDownload: 1 << 20, Lock: &oplock.Lock{},
		Launcher: e.launcher, Run: e.run, Now: e.clock.now, PPID: 42,
	}
	t.Cleanup(func() { e.launcher.exit(nil) })
	return e
}

// diskless makes the fixture root a diskless host with lbu.
func (e *svcEnv) diskless(t *testing.T) {
	t.Helper()
	writeFile(t, filepath.Join(e.root, "proc/mounts"), "tmpfs / tmpfs rw 0 0\n", 0o644)
	writeFile(t, filepath.Join(e.root, "sbin/lbu"), "", 0o755)
}

func (e *svcEnv) service() *Service { return New(e.opts) }

// run is the new binary: it says newVersion and makes the init script of service.Script.
func (e *svcEnv) run(_ context.Context, name string, args ...string) ([]byte, error) {
	e.mu.Lock()
	e.runs = append(e.runs, strings.Join(append([]string{name}, args...), " "))
	version, onRun := e.newVersion, e.onRun
	e.mu.Unlock()
	if onRun != nil {
		onRun(args)
	}
	switch {
	case len(args) == 1 && args[0] == "version":
		return []byte(version + "\n"), nil
	case len(args) == 4 && args[0] == "service" && args[1] == "script" && args[2] == "--binary":
		s, err := service.Script(args[3])
		return []byte(s), err
	}
	return nil, fmt.Errorf("unexpected command %s %v", name, args)
}

// lockFree tells whether the shared lock is free; it leaves it free.
func (e *svcEnv) lockFree() bool {
	if !e.opts.Lock.TryLock() {
		return false
	}
	e.opts.Lock.Unlock()
	return true
}

func (e *svcEnv) running(t *testing.T) bool {
	t.Helper()
	st, err := ReadState(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	return st.Running
}

// wantNothingLeft checks that an update that did not start left the shared lock and the update lock free and no
// file next to the binary or in the state directory but the lock file.
func (e *svcEnv) wantNothingLeft(t *testing.T) {
	t.Helper()
	if !e.lockFree() {
		t.Error("the shared lock is still held")
	}
	if e.running(t) {
		t.Error("the update lock is still held")
	}
	if got := entries(t, filepath.Dir(e.binary)); !slices.Equal(got, []string{"edelweissd"}) {
		t.Errorf("next to the binary: %v", got)
	}
	if es, err := os.ReadDir(e.dir); err == nil {
		for _, en := range es {
			if en.Name() != "lock" {
				t.Errorf("left in the state directory: %s", en.Name())
			}
		}
	}
}

func TestAboutForEverybody(t *testing.T) {
	e := newSvcEnv(t)
	s := e.service()
	a := s.About()
	if a.Version != "0.3.0" || a.Arch != "amd64" || a.Mode != Sys || !a.Possible || a.Reason != ReasonNone ||
		a.Repo != "Pukka922/edelweiss" || a.State.Running || a.State.Result != nil {
		t.Fatalf("About = %+v", a)
	}
	if n := e.gh.calls(); n != 0 {
		t.Fatalf("About asked GitHub %d times", n)
	}
	data, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != `{"version":"0.3.0","arch":"amd64","mode":"sys","possible":true,"repo":"Pukka922/edelweiss","state":{"running":false}}` {
		t.Fatalf("JSON = %s", got)
	}

	at := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	if err := WriteResult(e.dir, Result{From: "0.2.0", To: "0.3.0", Outcome: "updated", At: at}); err != nil {
		t.Fatal(err)
	}
	if r := s.About().State.Result; r == nil || r.Outcome != "updated" || r.To != "0.3.0" {
		t.Fatalf("the result: %+v", r)
	}

	e.diskless(t)
	if m := s.About().Mode; m != Diskless {
		t.Fatalf("mode = %q", m)
	}

	e.opts.Dev = true
	a = e.service().About()
	if a.Possible || a.Reason != ReasonDevMode {
		t.Fatalf("dev mode: %+v", a)
	}
	if data, _ := json.Marshal(a); !strings.Contains(string(data), `"possible":false,"reason":"dev_mode"`) {
		t.Fatalf("JSON = %s", data)
	}

	e.opts.Dev = false
	e.opts.Listen = "localhost"
	if a := e.service().About(); a.Possible || a.Reason != ReasonListenAddress {
		t.Fatalf("listen address without a port: %+v", a)
	}
}

func TestCheckCachedTwoMinutes(t *testing.T) {
	e := newSvcEnv(t)
	s := e.service()
	ctx := context.Background()
	start := e.clock.now()
	v, err := s.Check(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Offer.Available || v.Offer.Latest != "0.3.1" || v.Offer.SHA256 != svcDigest(svcContent) ||
		!v.CheckedAt.Equal(start) || v.AgeSeconds != 0 || v.Stale || v.LbuWarning {
		t.Fatalf("first check: %+v", v)
	}
	e.clock.advance(2*time.Minute - time.Second)
	if v, err = s.Check(ctx, false); err != nil || !v.CheckedAt.Equal(start) || v.AgeSeconds != 119 || e.gh.calls() != 1 {
		t.Fatalf("within two minutes: %+v, %v, %d calls", v, err, e.gh.calls())
	}
	e.clock.advance(time.Second)
	if v, err = s.Check(ctx, false); err != nil || !v.CheckedAt.Equal(e.clock.now()) || v.AgeSeconds != 0 || e.gh.calls() != 2 {
		t.Fatalf("after two minutes: %+v, %v, %d calls", v, err, e.gh.calls())
	}
	if _, err = s.Check(ctx, true); err != nil || e.gh.calls() != 3 {
		t.Fatalf("refresh: %v, %d calls", err, e.gh.calls())
	}
}

func TestCheckStaleOnRateLimit(t *testing.T) {
	e := newSvcEnv(t)
	s := e.service()
	ctx := context.Background()
	start := e.clock.now()
	if _, err := s.Check(ctx, false); err != nil {
		t.Fatal(err)
	}
	e.clock.advance(3 * time.Minute)
	for _, c := range []struct {
		err  error
		code string
	}{
		{fmt.Errorf("%w: reset at 11:00", github.ErrRateLimited), "github_rate_limited"},
		{fmt.Errorf("%w: status 502", github.ErrUnavailable), "github_unavailable"},
		{fmt.Errorf("%w: %w", github.ErrUnavailable, context.DeadlineExceeded), "timeout"},
		{context.Canceled, "timeout"},
	} {
		e.gh.fail(c.err)
		for _, refresh := range []bool{false, true} {
			v, err := s.Check(ctx, refresh)
			if err != nil || !v.Stale || v.StaleCode != c.code || v.StaleReason != c.err.Error() ||
				!v.CheckedAt.Equal(start) || v.AgeSeconds != 180 || v.Offer.Latest != "0.3.1" {
				t.Errorf("%v (refresh %v): %+v, %v", c.err, refresh, v, err)
			}
		}
	}
	// another failure is no reason to show the last result
	e.gh.fail(fmt.Errorf("%w: gone", github.ErrReleaseNotFound))
	if _, err := s.Check(ctx, false); !errors.Is(err, github.ErrReleaseNotFound) {
		t.Fatalf("release not found: %v", err)
	}
	// the stale answers did not replace the kept check, and a good one does
	e.gh.fail(nil)
	if v, err := s.Check(ctx, false); err != nil || v.Stale || !v.CheckedAt.Equal(e.clock.now()) {
		t.Fatalf("after GitHub is back: %+v, %v", v, err)
	}
	// without an earlier result the error is the answer
	e2 := newSvcEnv(t)
	e2.gh.fail(fmt.Errorf("%w: x", github.ErrRateLimited))
	if _, err := e2.service().Check(ctx, false); !errors.Is(err, github.ErrRateLimited) {
		t.Fatalf("no earlier result: %v", err)
	}
}

func TestCheckLbuWarning(t *testing.T) {
	e := newSvcEnv(t)
	ctx := context.Background()
	e.diskless(t)
	if v, err := e.service().Check(ctx, false); err != nil || !v.LbuWarning {
		t.Fatalf("diskless without the binary in lbu: %+v, %v", v, err)
	}
	writeFile(t, filepath.Join(e.root, "etc/apk/protected_paths.d/lbu.list"), "+"+strings.TrimPrefix(filepath.Dir(e.binary), "/")+"\n", 0o644)
	if v, err := e.service().Check(ctx, false); err != nil || v.LbuWarning {
		t.Fatalf("diskless with the binary in lbu: %+v, %v", v, err)
	}
}

func TestStartNotPossible(t *testing.T) {
	for _, c := range []struct {
		name   string
		edit   func(t *testing.T, e *svcEnv)
		reason Reason
	}{
		{"dev mode", func(_ *testing.T, e *svcEnv) { e.opts.Dev = true }, ReasonDevMode},
		{"dev build", func(_ *testing.T, e *svcEnv) { e.opts.Version = DevVersion }, ReasonDevBuild},
		{"not the service", func(_ *testing.T, e *svcEnv) { e.opts.PPID = 43 }, ReasonNotService},
		{"unknown mode", func(t *testing.T, e *svcEnv) { removeFile(t, filepath.Join(e.root, "proc/mounts")) }, ReasonUnknownMode},
		{"no lbu", func(t *testing.T, e *svcEnv) { e.diskless(t); removeFile(t, filepath.Join(e.root, "sbin/lbu")) }, ReasonNoLbu},
		{"listen address", func(_ *testing.T, e *svcEnv) { e.opts.Listen = "localhost" }, ReasonListenAddress},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newSvcEnv(t)
			c.edit(t, e)
			if a := e.service().About(); a.Possible || a.Reason != c.reason {
				t.Errorf("About = %+v; want not possible with %q", a, c.reason)
			}
			_, err := e.service().Start(context.Background(), svcDigest(svcContent))
			var np *NotPossibleError
			if !errors.Is(err, ErrNotPossible) || !errors.As(err, &np) || np.Reason != c.reason {
				t.Fatalf("Start = %v; want not possible with %q", err, c.reason)
			}
			if n := e.gh.calls(); n != 0 {
				t.Errorf("GitHub asked %d times", n)
			}
			if e.launcher.launched() != 0 {
				t.Error("the helper was launched")
			}
			e.wantNothingLeft(t)
		})
	}
}

func removeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

// A mode that cannot be read is logged once, not on every visit of the About page.
func TestAboutUnknownModeIsLoggedOnce(t *testing.T) {
	e := newSvcEnv(t)
	removeFile(t, filepath.Join(e.root, "proc/mounts"))
	logs := &strings.Builder{}
	var mu sync.Mutex
	e.opts.Logger = slog.New(slog.NewTextHandler(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return logs.Write(p)
	}), nil))
	s := e.service()
	for range 3 {
		if a := s.About(); a.Mode != "" || a.Possible || a.Reason != ReasonUnknownMode {
			t.Fatalf("About = %+v", a)
		}
	}
	if _, err := s.Start(context.Background(), svcDigest(svcContent)); !errors.Is(err, ErrNotPossible) {
		t.Fatalf("Start = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if n := strings.Count(logs.String(), "cannot tell the mode"); n != 1 {
		t.Fatalf("logged %d times:\n%s", n, logs.String())
	}
}

func TestStartNoUpdate(t *testing.T) {
	e := newSvcEnv(t)
	s := e.service()
	ctx := context.Background()
	if v, err := s.Check(ctx, false); err != nil || !v.Offer.Available {
		t.Fatalf("check: %+v, %v", v, err)
	}
	// the kept check still offers 0.3.1, the release says otherwise: Start asks again
	e.gh.publish("0.3.0", svcContent, svcDigest(svcContent))
	if _, err := s.Start(ctx, svcDigest(svcContent)); !errors.Is(err, ErrNoUpdate) {
		t.Fatalf("Start = %v; want ErrNoUpdate", err)
	}
	if n := e.gh.calls(); n != 2 {
		t.Errorf("GitHub asked %d times; want a fresh check", n)
	}
	if e.launcher.launched() != 0 || e.gh.downloaded() {
		t.Error("the update went on")
	}
	e.wantNothingLeft(t)
}

func TestStartBusyWithExtensionInstall(t *testing.T) {
	e := newSvcEnv(t)
	if !e.opts.Lock.TryLock() { // an extension install holds it
		t.Fatal("the lock is not free")
	}
	_, err := e.service().Start(context.Background(), svcDigest(svcContent))
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("Start = %v; want ErrBusy", err)
	}
	if e.launcher.launched() != 0 || e.gh.downloaded() {
		t.Error("the update went on")
	}
	if e.running(t) {
		t.Error("the update lock is held")
	}
	if e.lockFree() {
		t.Error("Start released a lock it did not take")
	}
	e.opts.Lock.Unlock()
	e.wantNothingLeft(t)
}

func TestStartRefusedWhileHelperRuns(t *testing.T) {
	e := newSvcEnv(t)
	held, err := AcquireLock(e.dir) // the helper of an earlier update, which outlived its edelweissd
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.service().Start(context.Background(), svcDigest(svcContent))
	if !errors.Is(err, ErrBusy) || !errors.Is(err, ErrHelperRunning) {
		t.Fatalf("Start = %v; want ErrBusy and ErrHelperRunning", err)
	}
	if e.launcher.launched() != 0 || e.gh.downloaded() {
		t.Error("the update went on")
	}
	if !e.lockFree() {
		t.Error("the shared lock is still held")
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	e.wantNothingLeft(t)
}

func TestStartLaunchesAndKeepsLock(t *testing.T) {
	e := newSvcEnv(t)
	s := e.service()
	ctx := context.Background()
	digest := svcDigest(svcContent)
	off, err := s.Start(ctx, digest)
	if err != nil {
		t.Fatal(err)
	}
	if off.Current != "0.3.0" || off.Latest != "0.3.1" || off.Asset != "edelweissd_0.3.1_linux_amd64" || off.SHA256 != digest {
		t.Fatalf("offer = %+v", off)
	}
	if e.launcher.plan != filepath.Join(e.dir, "plan.json") {
		t.Fatalf("launched with %q", e.launcher.plan)
	}
	data, err := os.ReadFile(e.launcher.plan)
	if err != nil {
		t.Fatal(err)
	}
	var p Plan
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	want := Plan{
		Binary: e.binary, Staged: e.binary + ".edelweiss-new", BinaryBackup: e.binary + ".edelweiss-bak",
		InitScript: filepath.Join(e.root, "etc/init.d/edelweiss"), InitBackup: filepath.Join(e.dir, "init.edelweiss-bak"),
		NewScript: filepath.Join(e.dir, "init.new"), From: "0.3.0", To: "0.3.1",
		HealthURL: "https://127.0.0.1:9090/api/health", Insecure: true, Lbu: false, RcService: "/usr/sbin/rc-service",
		Wait: 60 * time.Second, Stable: 30 * time.Second, Dir: e.dir,
	}
	if p != want {
		t.Fatalf("plan:\n got %+v\nwant %+v", p, want)
	}
	script, _ := service.Script(e.binary)
	wantFileMode(t, p.NewScript, script, 0o600)
	wantFileMode(t, p.Staged, string(svcContent), 0o755)
	if len(e.runs) != 2 || !strings.HasSuffix(e.runs[0], " version") || !strings.HasSuffix(e.runs[1], " service script --binary "+e.binary) {
		t.Errorf("runs = %q", e.runs)
	}

	if e.lockFree() {
		t.Fatal("the shared lock was released while the helper runs")
	}
	if !e.running(t) {
		t.Fatal("the helper does not hold the update lock")
	}
	if _, err := s.Start(ctx, digest); !errors.Is(err, ErrBusy) {
		t.Fatalf("a second Start = %v; want ErrBusy", err)
	}
	// only the helper holds the update lock: Start closed its own file
	e.launcher.mu.Lock()
	_ = e.launcher.held.Close()
	e.launcher.held = nil
	e.launcher.mu.Unlock()
	if e.running(t) {
		t.Fatal("Start still holds the update lock")
	}
}

func TestStartPlanForDiskless(t *testing.T) {
	e := newSvcEnv(t)
	e.diskless(t)
	e.opts.TLS, e.opts.Listen = false, "192.168.1.5:8080"
	if _, err := e.service().Start(context.Background(), svcDigest(svcContent)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(e.dir, "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	var p Plan
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	if !p.Lbu || p.LbuTool != "/sbin/lbu" || p.HealthURL != "http://192.168.1.5:8080/api/health" || p.Insecure {
		t.Fatalf("plan = %+v", p)
	}
}

func TestLockReleasedWhenHelperExitsEarly(t *testing.T) {
	e := newSvcEnv(t)
	if _, err := e.service().Start(context.Background(), svcDigest(svcContent)); err != nil {
		t.Fatal(err)
	}
	if e.lockFree() {
		t.Fatal("the shared lock is free while the helper runs")
	}
	e.launcher.exit(errors.New("exit status 1"))
	waitFor(t, 5*time.Second, "the shared lock", e.lockFree)
}

func TestStartErrorCleansUp(t *testing.T) {
	other := []byte("another binary")
	for _, c := range []struct {
		name      string
		edit      func(t *testing.T, e *svcEnv)
		confirmed string
		want      error
		launched  int
	}{
		{"checksum mismatch", func(t *testing.T, e *svcEnv) { e.gh.publish("0.3.1", svcContent, svcDigest(other)) },
			svcDigest(other), ErrChecksumMismatch, 0},
		{"release changed", nil, svcDigest(other), ErrReleaseChanged, 0},
		{"wrong version", func(t *testing.T, e *svcEnv) { e.newVersion = "0.3.2" }, "", ErrWrongVersion, 0},
		{"plan not written", func(t *testing.T, e *svcEnv) {
			writeFile(t, filepath.Join(e.dir, "init.new", "in-the-way"), "", 0o600)
		}, "", nil, 0},
		{"launch fails", func(t *testing.T, e *svcEnv) { e.launcher.err = errors.New("exec failed") }, "", nil, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newSvcEnv(t)
			if c.edit != nil {
				c.edit(t, e)
			}
			confirmed := c.confirmed
			if confirmed == "" {
				confirmed = svcDigest(svcContent)
			}
			_, err := e.service().Start(context.Background(), confirmed)
			if err == nil || (c.want != nil && !errors.Is(err, c.want)) {
				t.Fatalf("Start = %v; want %v", err, c.want)
			}
			if n := e.launcher.launched(); n != c.launched {
				t.Errorf("launched %d times", n)
			}
			if c.name == "plan not written" {
				if err := os.RemoveAll(filepath.Join(e.dir, "init.new")); err != nil {
					t.Fatal(err)
				}
			}
			e.wantNothingLeft(t)
		})
	}
}

// A launcher that gives no way to learn the end of the helper would keep the shared lock forever: Start undoes.
func TestStartRefusesALaunchWithoutDone(t *testing.T) {
	e := newSvcEnv(t)
	e.launcher.nilDone = true
	if _, err := e.service().Start(context.Background(), svcDigest(svcContent)); err == nil {
		t.Fatal("Start succeeded")
	}
	e.wantNothingLeft(t)
}

// startPanics runs Start and returns what it panicked with.
func startPanics(t *testing.T, s *Service) (v any) {
	t.Helper()
	defer func() { v = recover() }()
	_, _ = s.Start(context.Background(), svcDigest(svcContent))
	return nil
}

func TestStartPanicCleansUp(t *testing.T) {
	t.Run("in AcquireLock", func(t *testing.T) {
		e := newSvcEnv(t)
		old := acquireLock
		acquireLock = func(string) (*os.File, error) { panic("flock exploded") }
		t.Cleanup(func() { acquireLock = old })
		if v := startPanics(t, e.service()); v != "flock exploded" {
			t.Fatalf("recovered %v", v)
		}
		e.wantNothingLeft(t)
	})
	t.Run("in Prepare", func(t *testing.T) {
		e := newSvcEnv(t)
		e.onRun = func(args []string) {
			if args[0] == "service" {
				panic("the new binary exploded")
			}
		}
		if v := startPanics(t, e.service()); v != "the new binary exploded" {
			t.Fatalf("recovered %v", v)
		}
		e.wantNothingLeft(t)
	})
}

// The request ends (timeout, shutdown) while Prepare works: everything is undone.
func TestStartCancelledDuringPrepareCleansUp(t *testing.T) {
	t.Run("during the download", func(t *testing.T) {
		e := newSvcEnv(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		e.gh.onBinary = func(ctx context.Context) error {
			cancel()
			return fmt.Errorf("%w: download: %w", github.ErrUnavailable, ctx.Err()) // as the GitHub client reports it
		}
		if _, err := e.service().Start(ctx, svcDigest(svcContent)); !errors.Is(err, context.Canceled) {
			t.Fatalf("Start = %v; want context.Canceled", err)
		}
		e.wantNothingLeft(t)
	})
	t.Run("while the new binary runs", func(t *testing.T) {
		e := newSvcEnv(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		e.onRun = func(args []string) {
			if args[0] == "version" {
				cancel()
			}
		}
		if _, err := e.service().Start(ctx, svcDigest(svcContent)); !errors.Is(err, context.Canceled) {
			t.Fatalf("Start = %v; want context.Canceled", err)
		}
		e.wantNothingLeft(t)
	})
}

// helperLock is the shared lock as the daemon builds it: it is busy while a helper holds the update lock of dir.
func helperLock(dir string) *oplock.Lock {
	return oplock.New(func() bool { return HelperRunning(dir) })
}

func TestHelperRunning(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "self-update")
	if HelperRunning(dir) {
		t.Fatal("running without a directory")
	}
	held, err := AcquireLock(dir) // another open file: the helper's
	if err != nil {
		t.Fatal(err)
	}
	if !HelperRunning(dir) {
		t.Fatal("not running while the lock is held")
	}
	if l := helperLock(dir); l.TryLock() {
		t.Fatal("the shared lock was granted while the helper runs")
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	if HelperRunning(dir) {
		t.Fatal("running after the lock was released")
	}
	if l := helperLock(dir); !l.TryLock() {
		t.Fatal("the shared lock was refused after the helper ended")
	}
}

// The probe takes the lock for a moment, as ReadState does: a page polling the state never makes it report a helper.
func TestHelperRunningSurvivesProbes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "self-update")
	first, err := AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	first.Close()
	stop := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for {
			select {
			case <-stop:
				return
			default:
				_, _ = ReadState(dir)        // takes the lock for a moment
				time.Sleep(time.Millisecond) // a page polls far less often than this
			}
		}
	}()
	defer func() { close(stop); <-finished }()
	l := helperLock(dir)
	for i := 0; i < 200; i++ {
		if !l.TryLock() {
			t.Fatalf("attempt %d: refused", i)
		}
		l.Unlock()
	}
}

// After the restart the new edelweissd has a fresh shared lock while the helper still works (the second probe, lbu
// commit): the probe of the lock refuses the update as it refuses an extension install, and Start's own update lock
// does not trip it.
func TestStartRefusedBySharedLockWhileHelperRuns(t *testing.T) {
	e := newSvcEnv(t)
	e.opts.Lock = helperLock(e.dir)
	held, err := AcquireLock(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	s := e.service()
	_, err = s.Start(context.Background(), svcDigest(svcContent))
	if !errors.Is(err, ErrBusy) || errors.Is(err, ErrHelperRunning) {
		t.Fatalf("Start = %v; want ErrBusy from the shared lock", err)
	}
	if e.launcher.launched() != 0 || e.gh.downloaded() {
		t.Error("the update went on")
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start(context.Background(), svcDigest(svcContent)); err != nil {
		t.Fatalf("Start after the helper ended: %v", err)
	}
	if e.lockFree() {
		t.Fatal("the shared lock is free while the new helper runs")
	}
	e.launcher.exit(nil)
	waitFor(t, 5*time.Second, "the shared lock", e.lockFree)
}
