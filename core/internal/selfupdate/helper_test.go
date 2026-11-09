package selfupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	oldBinary  = "edelweissd 0.3.0"
	newBinary  = "edelweissd 0.3.1"
	oldScript  = "#!/sbin/openrc-run\n# 0.3.0\n"
	newScript  = "#!/sbin/openrc-run\n# 0.3.1\n"
	restartCmd = "run /sbin/rc-service edelweiss restart"
	lbuCmd     = "run /sbin/lbu commit"
)

var helperEpoch = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

// world is the host of a helper test: a temp root with the files of the plan, a fake clock that only Sleep (and a
// slow probe) moves, a fake rc-service whose restart starts whatever binary is on disk at that moment, and a health
// endpoint that answers for the version that runs.
type world struct {
	root, planPath string
	plan           Plan
	now            time.Time
	log            io.Writer
	logBuf         bytes.Buffer

	runErr     func(line string, nth int) error                           // nil: every command succeeds
	onRestart  func(w *world, nth int)                                    // before the nth restart takes effect
	beforeRun  func()                                                     // before each command
	newAnswer  func(w *world, ctx context.Context, n int) (Health, error) // the new version's nth answer; nil: healthy
	oldDown    bool                                                       // the old version never answers
	staleOld   int                                                        // after the restart the old process still answers this often
	probePanic bool                                                       // every probe panics

	running string    // the version the fake service runs, "" while it is down
	started time.Time // and when it started

	events        []string // "run <command line>", "probe", "sleep <d>" in order
	runs          map[string]int
	runTimeouts   []time.Duration
	lockAtRun     []bool   // whether the update lock was held during each command
	atRestart     []string // "<binary>|<init script>" on disk at each restart
	restartAt     []time.Time
	newProbes     int
	probeTimeouts []time.Duration
	probeTargets  []string
}

// newWorld lays out bin/edelweissd (the old version), bin/edelweissd.edelweiss-new (the new one), etc/init.d/edelweiss
// (the old script) and the plan with init.new (the new script); the init backup goes to the plan directory. The lock is
// taken afresh by the helper: a test runs in one process and has no fd 3 to hand on.
func newWorld(t *testing.T, edit func(p *Plan)) *world {
	t.Helper()
	root := t.TempDir()
	bin, initd := filepath.Join(root, "bin"), filepath.Join(root, "etc", "init.d")
	p := Plan{
		Binary: filepath.Join(bin, "edelweissd"), Staged: filepath.Join(bin, "edelweissd.edelweiss-new"),
		BinaryBackup: filepath.Join(bin, "edelweissd.edelweiss-bak"), InitScript: filepath.Join(initd, "edelweiss"),
		From: "0.3.0", To: "0.3.1",
		HealthURL: "http://127.0.0.1:8080/api/health", RcService: "/sbin/rc-service", LbuTool: "/sbin/lbu",
		Wait: 60 * time.Second, Stable: 30 * time.Second, Dir: filepath.Join(root, "state", "self-update"),
		Script: newScript,
	}
	p.NewScript = filepath.Join(p.Dir, "init.new")
	p.InitBackup = filepath.Join(p.Dir, "init.edelweiss-bak")
	if edit != nil {
		edit(&p)
	}
	writeFile(t, p.Binary, oldBinary, 0o755)
	writeFile(t, p.Staged, newBinary, 0o755)
	writeFile(t, p.InitScript, oldScript, 0o755)
	// the cgroup of the service, and an escape directory whose cgroup.procs is that file: writing the pid moves the
	// process out of it, as on a host where the move works
	writeFile(t, filepath.Join(root, "cgroup"), "0::/openrc.edelweiss\n", 0o644)
	if err := os.MkdirAll(filepath.Join(root, "escape"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "cgroup"), filepath.Join(root, "escape", "cgroup.procs")); err != nil {
		t.Fatal(err)
	}
	path, err := WritePlan(p)
	if err != nil {
		t.Fatal(err)
	}
	p.Script = ""
	w := &world{root: root, planPath: path, plan: p, now: helperEpoch, runs: map[string]int{}}
	w.log = &w.logBuf
	oldAdopt, oldRename := adoptLock, rename
	adoptLock = func(dir string) (*os.File, error) { return AcquireLock(dir) }
	t.Cleanup(func() { adoptLock, rename = oldAdopt, oldRename })
	return w
}

func (w *world) deps() HelperDeps {
	return HelperDeps{
		Run: w.run, Probe: w.probe, Now: func() time.Time { return w.now }, Sleep: w.sleep, Log: w.log,
		Cgroup: filepath.Join(w.root, "cgroup"), Escape: filepath.Join(w.root, "escape"),
	}
}

func (w *world) runHelper() int { return RunHelper(w.planPath, w.deps()) }

func (w *world) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if w.beforeRun != nil {
		w.beforeRun()
	}
	line := strings.Join(append([]string{name}, args...), " ")
	w.events = append(w.events, "run "+line)
	timeout := time.Duration(-1)
	if dl, ok := ctx.Deadline(); ok {
		timeout = time.Until(dl)
	}
	w.runTimeouts = append(w.runTimeouts, timeout)
	w.lockAtRun = append(w.lockAtRun, lockHeld(filepath.Join(w.plan.Dir, "lock")))
	nth := w.runs[line]
	w.runs[line]++
	var err error
	if w.runErr != nil {
		err = w.runErr(line, nth)
	}
	if line == w.plan.RcService+" edelweiss restart" {
		if w.onRestart != nil {
			w.onRestart(w, nth)
		}
		bin, _ := os.ReadFile(w.plan.Binary)
		script, _ := os.ReadFile(w.plan.InitScript)
		w.atRestart = append(w.atRestart, string(bin)+"|"+string(script))
		w.restartAt = append(w.restartAt, w.now)
		w.running, w.started = "", time.Time{}
		if err == nil {
			w.running, w.started = strings.TrimPrefix(string(bin), "edelweissd "), w.now
		}
	}
	return nil, err
}

var errRefused = errors.New("connection refused")

func (w *world) probe(ctx context.Context, url string, insecure bool) (Health, error) {
	if w.probePanic {
		panic("the probe exploded")
	}
	w.events = append(w.events, "probe")
	timeout := time.Duration(-1)
	if dl, ok := ctx.Deadline(); ok {
		timeout = time.Until(dl)
	}
	w.probeTimeouts = append(w.probeTimeouts, timeout)
	w.probeTargets = append(w.probeTargets, fmt.Sprintf("%s insecure=%v", url, insecure))
	switch {
	case w.running == "":
	case w.running == w.plan.To && w.staleOld > 0:
		w.staleOld--
		return Health{Version: w.plan.From, Started: helperEpoch.Add(-time.Hour)}, nil
	case w.running == w.plan.To:
		n := w.newProbes
		w.newProbes++
		if w.newAnswer != nil {
			return w.newAnswer(w, ctx, n)
		}
		return Health{Version: w.running, Started: w.started}, nil
	case !w.oldDown:
		return Health{Version: w.running, Started: w.started}, nil
	}
	return Health{}, errRefused
}

func (w *world) sleep(d time.Duration) {
	w.events = append(w.events, "sleep "+d.String())
	w.now = w.now.Add(d)
}

func (w *world) result(t *testing.T) Result {
	t.Helper()
	st, err := ReadState(w.plan.Dir)
	if err != nil || st.Result == nil {
		t.Fatalf("no result: %+v, %v; log:\n%s", st, err, w.logBuf.String())
	}
	if st.Running {
		t.Error("the update lock is still held after the helper ended")
	}
	return *st.Result
}

func (w *world) helperLog(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(w.plan.Dir, "helper.log"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// wantFile checks content and mode 0755.
func wantFile(t *testing.T, path, content string) {
	t.Helper()
	wantFileMode(t, path, content, 0o755)
}

func wantFileMode(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	data, err := os.ReadFile(path)
	fi, serr := os.Stat(path)
	if err != nil || serr != nil || string(data) != content || fi.Mode().Perm() != mode {
		var got os.FileMode
		if fi != nil {
			got = fi.Mode()
		}
		t.Errorf("%s = %q, %v (%v); want %q, %v", path, data, got, err, content, mode)
	}
}

func wantNoFile(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s is still there (%v)", path, err)
	}
}

// wantNoTemp checks that no temporary file of the helper (a dot file) is left in dir.
func wantNoTemp(t *testing.T, dir string) {
	t.Helper()
	for _, name := range entries(t, dir) {
		if strings.HasPrefix(name, ".") {
			t.Errorf("a temporary file is left in %s: %s", dir, name)
		}
	}
}

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permissions this test relies on")
	}
}

func refuse(*world, context.Context, int) (Health, error) { return Health{}, errRefused }

func failRestartOnce(line string, nth int) error {
	if line == "/sbin/rc-service edelweiss restart" && nth == 0 {
		return errors.New("rc-service: ERROR: edelweiss failed to start")
	}
	return nil
}

// Before the first file is replaced, the log names the manual way back, for an update that is interrupted (the host
// loses power, the helper is killed): README and syslog say the same.
func TestHelperLogsTheManualCommandsBeforeTheSwap(t *testing.T) {
	w := newWorld(t, nil)
	if code := w.runHelper(); code != 0 {
		t.Fatalf("exit %d, log:\n%s", code, w.logBuf.String())
	}
	log := w.logBuf.String()
	wantManualCommands(t, w, log)
	notice, swap := strings.Index(log, manualCommands(w)[0]), strings.Index(log, "putting "+w.plan.Staged)
	if notice < 0 || swap < 0 || notice > swap {
		t.Errorf("the commands (at %d) do not come before the swap (at %d):\n%s", notice, swap, log)
	}
}

func TestHelperUpdates(t *testing.T) {
	w := newWorld(t, nil)
	if code := w.runHelper(); code != 0 {
		t.Fatalf("exit %d, log:\n%s", code, w.logBuf.String())
	}
	if want := []string{restartCmd, "probe", "sleep 30s", "probe"}; !slices.Equal(w.events, want) {
		t.Errorf("events = %q, want %q", w.events, want)
	}
	if len(w.atRestart) != 1 || w.atRestart[0] != newBinary+"|"+newScript {
		t.Errorf("on disk at the restart: %q, want the new binary and script", w.atRestart)
	}
	wantFile(t, w.plan.Binary, newBinary)
	wantFile(t, w.plan.InitScript, newScript)
	wantFile(t, w.plan.BinaryBackup, oldBinary)
	wantFileMode(t, w.plan.InitBackup, oldScript, 0o644)
	wantNoFile(t, w.plan.Staged)
	if got, want := entries(t, filepath.Dir(w.plan.Binary)), []string{"edelweissd", "edelweissd.edelweiss-bak"}; !slices.Equal(got, want) {
		t.Errorf("bin = %q, want %q", got, want)
	}
	// nothing but the script in /etc/init.d, where OpenRC would list a backup as a service
	if got, want := entries(t, filepath.Dir(w.plan.InitScript)), []string{"edelweiss"}; !slices.Equal(got, want) {
		t.Errorf("init.d = %q, want %q", got, want)
	}
	if got, want := entries(t, w.plan.Dir), []string{"helper.log", "init.edelweiss-bak", "init.new", "lock", "plan.json", "result.json"}; !slices.Equal(got, want) {
		t.Errorf("state = %q, want %q", got, want)
	}
	r := w.result(t)
	if r.From != "0.3.0" || r.To != "0.3.1" || r.Outcome != "updated" || r.Reason != "" || r.LbuError != "" || !r.At.Equal(w.now) {
		t.Errorf("result = %+v, at %v", r, w.now)
	}
	if !slices.Equal(w.lockAtRun, []bool{true}) {
		t.Errorf("the lock during the commands: %v, want held", w.lockAtRun)
	}
	if d := w.runTimeouts[0]; d <= 119*time.Second || d > 2*time.Minute {
		t.Errorf("rc-service has %v, want 2 min", d)
	}
	for i, d := range w.probeTimeouts {
		if d <= 2*time.Second || d > 3*time.Second {
			t.Errorf("probe %d has %v, want 3 s", i, d)
		}
	}
	for _, target := range w.probeTargets {
		if target != "http://127.0.0.1:8080/api/health insecure=false" {
			t.Errorf("probed %s", target)
		}
	}
	// out of the cgroup of the service
	if procs, err := os.ReadFile(filepath.Join(w.root, "escape", "cgroup.procs")); err != nil || string(procs) != strconv.Itoa(os.Getpid()) {
		t.Errorf("cgroup.procs = %q, %v; want %d", procs, err, os.Getpid())
	}
	if log := w.logBuf.String(); !strings.Contains(log, "0.3.1") || !strings.Contains(log, "updated") {
		t.Errorf("log:\n%s", log)
	}
}

// The new version is not up at once: refused connections first, then it answers.
func TestHelperUpdatesAfterRefusedProbes(t *testing.T) {
	w := newWorld(t, nil)
	w.newAnswer = func(w *world, _ context.Context, n int) (Health, error) {
		if n < 3 {
			return Health{}, errRefused
		}
		return Health{Version: "0.3.1", Started: w.started}, nil
	}
	if code := w.runHelper(); code != 0 {
		t.Fatalf("exit %d, log:\n%s", code, w.logBuf.String())
	}
	want := []string{restartCmd, "probe", "sleep 1s", "probe", "sleep 1s", "probe", "sleep 1s", "probe", "sleep 30s", "probe"}
	if !slices.Equal(w.events, want) {
		t.Errorf("events = %q, want %q", w.events, want)
	}
	if r := w.result(t); r.Outcome != "updated" {
		t.Errorf("result = %+v", r)
	}
	wantFile(t, w.plan.Binary, newBinary)
}

// After the restart the old process still answers for a while, with the old version: the helper waits for the new.
func TestHelperWaitsWhileTheOldProcessAnswers(t *testing.T) {
	w := newWorld(t, nil)
	w.staleOld = 3
	if code := w.runHelper(); code != 0 {
		t.Fatalf("exit %d, log:\n%s", code, w.logBuf.String())
	}
	want := []string{restartCmd, "probe", "sleep 1s", "probe", "sleep 1s", "probe", "sleep 1s", "probe", "sleep 30s", "probe"}
	if !slices.Equal(w.events, want) {
		t.Errorf("events = %q, want %q", w.events, want)
	}
	if r := w.result(t); r.Outcome != "updated" {
		t.Errorf("result = %+v", r)
	}
	if !strings.Contains(w.logBuf.String(), `version "0.3.0" answers`) {
		t.Errorf("log:\n%s", w.logBuf.String())
	}
}

func TestHelperLbuInDiskless(t *testing.T) {
	w := newWorld(t, func(p *Plan) { p.Lbu, p.HealthURL, p.Insecure = true, "https://127.0.0.1:8443/api/health", true })
	if code := w.runHelper(); code != 0 {
		t.Fatalf("exit %d, log:\n%s", code, w.logBuf.String())
	}
	if want := []string{restartCmd, "probe", "sleep 30s", "probe", lbuCmd}; !slices.Equal(w.events, want) {
		t.Errorf("events = %q, want %q", w.events, want)
	}
	if d := w.runTimeouts[1]; d <= 119*time.Second || d > 2*time.Minute {
		t.Errorf("lbu commit has %v, want 2 min", d)
	}
	if !slices.Equal(w.lockAtRun, []bool{true, true}) {
		t.Errorf("the lock during the commands: %v", w.lockAtRun)
	}
	for _, target := range w.probeTargets {
		if target != "https://127.0.0.1:8443/api/health insecure=true" {
			t.Errorf("probed %s", target)
		}
	}
	if r := w.result(t); r.Outcome != "updated" || r.LbuError != "" {
		t.Errorf("result = %+v", r)
	}
	wantFile(t, w.plan.Binary, newBinary)
}

func TestHelperLbuFailureIsNoted(t *testing.T) {
	w := newWorld(t, func(p *Plan) { p.Lbu = true })
	w.runErr = func(line string, _ int) error {
		if line == "/sbin/lbu commit" {
			return errors.New("lbu: no space left on the medium")
		}
		return nil
	}
	if code := w.runHelper(); code != 0 {
		t.Fatalf("exit %d, log:\n%s", code, w.logBuf.String())
	}
	r := w.result(t)
	if r.Outcome != "updated" || r.Reason != "" || !strings.Contains(r.LbuError, "no space left on the medium") {
		t.Errorf("result = %+v", r)
	}
	wantFile(t, w.plan.Binary, newBinary)
	wantFile(t, w.plan.InitScript, newScript)
	if !strings.Contains(w.logBuf.String(), "no space left on the medium") {
		t.Errorf("log:\n%s", w.logBuf.String())
	}
}

// wantRolledBack checks the result and that the old binary and script are back, were back before the last restart,
// and that the backups stay.
func wantRolledBack(t *testing.T, w *world, reason string) {
	t.Helper()
	r := w.result(t)
	if r.From != "0.3.0" || r.To != "0.3.1" || r.Outcome != "rolled_back" || r.Reason != reason || r.LbuError != "" || !r.At.Equal(w.now) {
		t.Errorf("result = %+v, want rolled_back, %s, at %v", r, reason, w.now)
	}
	wantFile(t, w.plan.Binary, oldBinary)
	wantFile(t, w.plan.InitScript, oldScript)
	wantFile(t, w.plan.BinaryBackup, oldBinary)
	wantFileMode(t, w.plan.InitBackup, oldScript, 0o644)
	wantNoTemp(t, filepath.Dir(w.plan.Binary))
	wantNoTemp(t, filepath.Dir(w.plan.InitScript))
	if n := len(w.atRestart); n == 0 || w.atRestart[n-1] != oldBinary+"|"+oldScript {
		t.Errorf("on disk at the restarts: %q, want the old binary and script at the last", w.atRestart)
	}
	if last := w.events[len(w.events)-1]; last != "probe" {
		t.Errorf("the last event is %q, want the probe that found the old version", last)
	}
	if slices.Contains(w.events, lbuCmd) {
		t.Error("lbu commit after a rollback")
	}
	if log := w.logBuf.String(); !strings.Contains(log, reason) || !strings.Contains(log, "rolled back") {
		t.Errorf("log:\n%s", log)
	}
}

func TestHelperRollsBackWhenNotHealthy(t *testing.T) {
	for _, c := range []struct {
		name   string
		answer func(w *world, ctx context.Context, n int) (Health, error)
	}{
		{"no answer", refuse},
		{"another version answers", func(w *world, _ context.Context, _ int) (Health, error) {
			return Health{Version: "0.2.9", Started: w.started}, nil
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t, func(p *Plan) { p.Lbu = true })
			w.newAnswer = c.answer
			if code := w.runHelper(); code != 1 {
				t.Fatalf("exit %d, log:\n%s", code, w.logBuf.String())
			}
			wantRolledBack(t, w, "not_healthy")
			wantNoFile(t, w.plan.Staged)
			if len(w.restartAt) != 2 {
				t.Fatalf("%d restarts, want 2", len(w.restartAt))
			}
			if waited := w.restartAt[1].Sub(w.restartAt[0]); waited < 60*time.Second || waited > 61*time.Second {
				t.Errorf("waited %v for the new version, want 60 s", waited)
			}
			if w.newProbes < 59 || w.newProbes > 61 {
				t.Errorf("%d probes of the new version, want one a second for 60 s", w.newProbes)
			}
			for _, e := range w.events {
				if strings.HasPrefix(e, "sleep ") && e != "sleep 1s" {
					t.Errorf("event %q, want only 1 s between probes", e)
				}
			}
		})
	}
}

// hang is a probe that hangs until its deadline. The fake clock moves by the time to it, rounded, as the real clock
// has moved on a little since the deadline was set.
func hang(w *world, ctx context.Context, _ int) (Health, error) {
	dl, ok := ctx.Deadline()
	if !ok {
		w.now = w.now.Add(time.Hour) // a probe without a deadline hangs for good
		return Health{}, errors.New("no deadline")
	}
	w.now = w.now.Add(time.Until(dl).Round(time.Millisecond))
	return Health{}, context.DeadlineExceeded
}

// A probe that hangs takes at most 3 s, the next comes 1 s later, and the wait still ends after 60 s.
func TestHelperWaitIsBounded(t *testing.T) {
	w := newWorld(t, nil)
	w.newAnswer = hang
	if code := w.runHelper(); code != 1 {
		t.Fatalf("exit %d, log:\n%s", code, w.logBuf.String())
	}
	wantRolledBack(t, w, "not_healthy")
	if len(w.restartAt) != 2 {
		t.Fatalf("%d restarts, want 2", len(w.restartAt))
	}
	if waited := w.restartAt[1].Sub(w.restartAt[0]); waited != 60*time.Second {
		t.Errorf("waited %v for the new version, want 60 s", waited)
	}
	if w.newProbes != 15 {
		t.Errorf("%d probes of the new version, want 15 (3 s each, 1 s apart)", w.newProbes)
	}
	for i, d := range w.probeTimeouts {
		if d < 0 || d > 3*time.Second {
			t.Errorf("probe %d has %v, want at most 3 s", i, d)
		}
	}
}

// Less than 3 s left: the probe gets only what is left.
func TestHelperProbeTimeoutIsCappedAtTheDeadline(t *testing.T) {
	w := newWorld(t, func(p *Plan) { p.Wait = 2500 * time.Millisecond })
	w.newAnswer = hang
	if code := w.runHelper(); code != 1 {
		t.Fatalf("exit %d, log:\n%s", code, w.logBuf.String())
	}
	wantRolledBack(t, w, "not_healthy")
	if w.newProbes != 1 {
		t.Errorf("%d probes of the new version, want 1", w.newProbes)
	}
	if d := w.probeTimeouts[0]; d < 2400*time.Millisecond || d > 2500*time.Millisecond {
		t.Errorf("the probe has %v, want the 2.5 s left", d)
	}
	if waited := w.restartAt[1].Sub(w.restartAt[0]); waited != 2500*time.Millisecond {
		t.Errorf("waited %v, want 2.5 s", waited)
	}
}

func TestHelperRollsBackWhenUnstable(t *testing.T) {
	for _, c := range []struct {
		name   string
		second func(w *world) (Health, error)
	}{
		{"it started again", func(w *world) (Health, error) {
			return Health{Version: "0.3.1", Started: w.started.Add(7 * time.Second)}, nil
		}},
		{"it does not answer", func(*world) (Health, error) { return Health{}, errRefused }},
		{"another version answers", func(w *world) (Health, error) {
			return Health{Version: "0.3.0", Started: w.started}, nil
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t, nil)
			w.newAnswer = func(w *world, _ context.Context, n int) (Health, error) {
				if n == 0 {
					return Health{Version: "0.3.1", Started: w.started}, nil
				}
				return c.second(w)
			}
			if code := w.runHelper(); code != 1 {
				t.Fatalf("exit %d, log:\n%s", code, w.logBuf.String())
			}
			if want := []string{restartCmd, "probe", "sleep 30s", "probe", restartCmd, "probe"}; !slices.Equal(w.events, want) {
				t.Errorf("events = %q, want %q", w.events, want)
			}
			wantRolledBack(t, w, "unstable")
			wantNoFile(t, w.plan.Staged)
		})
	}
}

func TestHelperRollsBackWhenRestartFails(t *testing.T) {
	w := newWorld(t, nil)
	w.runErr = failRestartOnce
	if code := w.runHelper(); code != 1 {
		t.Fatalf("exit %d, log:\n%s", code, w.logBuf.String())
	}
	if want := []string{restartCmd, restartCmd, "probe"}; !slices.Equal(w.events, want) {
		t.Errorf("events = %q, want %q", w.events, want)
	}
	if len(w.atRestart) != 2 || w.atRestart[0] != newBinary+"|"+newScript {
		t.Errorf("on disk at the restarts: %q", w.atRestart)
	}
	wantRolledBack(t, w, "restart_failed")
	wantNoFile(t, w.plan.Staged)
}

// The very first rename fails: nothing was replaced, so there is nothing to roll back and no restart.
func TestHelperFirstRenameFailureChangesNothing(t *testing.T) {
	w := newWorld(t, nil)
	rename = func(from, to string) error {
		if from == w.plan.Staged {
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: syscall.EXDEV}
		}
		return os.Rename(from, to)
	}
	if code := w.runHelper(); code != 1 {
		t.Fatalf("exit %d, log:\n%s", code, w.logBuf.String())
	}
	if len(w.events) != 0 {
		t.Errorf("events = %q, want none", w.events)
	}
	if r := w.result(t); r.Outcome != "failed" || r.Reason != "swap_failed" {
		t.Errorf("result = %+v, want failed, swap_failed", r)
	}
	wantFile(t, w.plan.Binary, oldBinary)
	wantFile(t, w.plan.InitScript, oldScript)
	wantNoFile(t, w.plan.Staged)
}

// The init script cannot be replaced after the binary was: only the binary is put back. A backup of the init script
// that cannot be read does not matter, as the script was never replaced.
func TestHelperInitWriteFailureRollsBackTheBinary(t *testing.T) {
	for _, unreadable := range []bool{false, true} {
		t.Run(fmt.Sprintf("init backup unreadable=%v", unreadable), func(t *testing.T) {
			if unreadable {
				skipIfRoot(t)
			}
			w := newWorld(t, nil)
			failed := false
			rename = func(from, to string) error {
				if to == w.plan.InitScript && !failed {
					failed = true
					if unreadable {
						_ = os.Chmod(w.plan.InitBackup, 0)
					}
					return &os.LinkError{Op: "rename", Old: from, New: to, Err: syscall.ENOSPC}
				}
				return os.Rename(from, to)
			}
			if code := w.runHelper(); code != 1 {
				t.Fatalf("exit %d, log:\n%s", code, w.logBuf.String())
			}
			if want := []string{restartCmd, "probe"}; !slices.Equal(w.events, want) {
				t.Errorf("events = %q, want %q", w.events, want)
			}
			if unreadable {
				_ = os.Chmod(w.plan.InitBackup, 0o644)
			}
			wantRolledBack(t, w, "swap_failed")
			wantNoFile(t, w.plan.Staged)
		})
	}
}

// The copy of the backup fails (here: it cannot be read; on a full tmpfs: no room for one more binary). The backup is
// moved into place instead: the old version comes back, its backup is gone.
func TestHelperRollbackMovesABackupItCannotCopy(t *testing.T) {
	skipIfRoot(t)
	w := newWorld(t, nil)
	w.newAnswer = refuse
	w.onRestart = func(w *world, nth int) {
		if nth == 0 {
			_ = os.Chmod(w.plan.BinaryBackup, 0)
		}
	}
	if code := w.runHelper(); code != 1 {
		t.Fatalf("exit %d, log:\n%s", code, w.logBuf.String())
	}
	if r := w.result(t); r.Outcome != "rolled_back" || r.Reason != "not_healthy" {
		t.Errorf("result = %+v", r)
	}
	wantFile(t, w.plan.Binary, oldBinary)
	wantFile(t, w.plan.InitScript, oldScript)
	wantNoFile(t, w.plan.BinaryBackup)
	wantFileMode(t, w.plan.InitBackup, oldScript, 0o644)
	wantNoTemp(t, filepath.Dir(w.plan.Binary))
	if n := len(w.atRestart); n != 2 || w.atRestart[1] != oldBinary+"|"+oldScript {
		t.Errorf("on disk at the restarts: %q", w.atRestart)
	}
	if !strings.Contains(w.logBuf.String(), "the backup is then gone") {
		t.Errorf("log:\n%s", w.logBuf.String())
	}
}

func manualCommands(w *world) []string {
	return []string{
		"mv " + w.plan.BinaryBackup + " " + w.plan.Binary,
		"install -m 755 " + w.plan.InitBackup + " " + w.plan.InitScript,
		"rc-service edelweiss restart",
	}
}

func wantManualCommands(t *testing.T, w *world, log string) {
	t.Helper()
	for _, cmd := range manualCommands(w) {
		if !strings.Contains(log, cmd) {
			t.Errorf("the log does not name %q:\n%s", cmd, log)
		}
	}
}

func TestHelperRollbackFailed(t *testing.T) {
	t.Run("the old version does not come back", func(t *testing.T) {
		w := newWorld(t, nil)
		w.newAnswer, w.oldDown = refuse, true
		if code := w.runHelper(); code != 2 {
			t.Fatalf("exit %d, log:\n%s", code, w.logBuf.String())
		}
		r := w.result(t)
		if r.Outcome != "rollback_failed" || r.Reason != "not_healthy" || r.From != "0.3.0" || r.To != "0.3.1" || !r.At.Equal(w.now) {
			t.Errorf("result = %+v", r)
		}
		wantManualCommands(t, w, w.logBuf.String())
		// the files were put back; only the old version did not answer
		wantFile(t, w.plan.Binary, oldBinary)
		wantFile(t, w.plan.InitScript, oldScript)
		wantFile(t, w.plan.BinaryBackup, oldBinary)
		wantFileMode(t, w.plan.InitBackup, oldScript, 0o644)
		if len(w.restartAt) != 2 {
			t.Fatalf("%d restarts, want 2", len(w.restartAt))
		}
		if waited := w.now.Sub(w.restartAt[1]); waited < 60*time.Second || waited > 61*time.Second {
			t.Errorf("waited %v for the old version, want 60 s", waited)
		}
	})
	t.Run("the binary cannot be put back", func(t *testing.T) {
		skipIfRoot(t)
		w := newWorld(t, nil)
		binDir := filepath.Dir(w.plan.Binary)
		w.runErr = failRestartOnce
		w.onRestart = func(_ *world, nth int) {
			if nth == 0 {
				_ = os.Chmod(binDir, 0o555)
			}
		}
		t.Cleanup(func() { _ = os.Chmod(binDir, 0o755) })
		if code := w.runHelper(); code != 2 {
			t.Fatalf("exit %d, log:\n%s", code, w.logBuf.String())
		}
		if r := w.result(t); r.Outcome != "rollback_failed" || r.Reason != "restart_failed" {
			t.Errorf("result = %+v", r)
		}
		wantManualCommands(t, w, w.logBuf.String())
		wantFile(t, w.plan.BinaryBackup, oldBinary)
		wantFileMode(t, w.plan.InitBackup, oldScript, 0o644)
		wantFile(t, w.plan.InitScript, oldScript)
		wantNoTemp(t, binDir)
	})
	// the old version runs again, but with the new init script: not a clean rollback, the admin has to finish it
	t.Run("the init script cannot be put back", func(t *testing.T) {
		skipIfRoot(t)
		w := newWorld(t, nil)
		initDir := filepath.Dir(w.plan.InitScript)
		w.newAnswer = refuse
		w.onRestart = func(_ *world, nth int) {
			if nth == 0 {
				_ = os.Chmod(initDir, 0o555)
			}
		}
		t.Cleanup(func() { _ = os.Chmod(initDir, 0o755) })
		if code := w.runHelper(); code != 2 {
			t.Fatalf("exit %d, log:\n%s", code, w.logBuf.String())
		}
		if r := w.result(t); r.Outcome != "rollback_failed" || r.Reason != "not_healthy" {
			t.Errorf("result = %+v", r)
		}
		if w.running != "0.3.0" {
			t.Errorf("the fake service runs %q, want the old version", w.running)
		}
		wantManualCommands(t, w, w.logBuf.String())
		wantFile(t, w.plan.Binary, oldBinary)
		wantFile(t, w.plan.InitScript, newScript)
		wantFileMode(t, w.plan.InitBackup, oldScript, 0o644)
		wantNoTemp(t, initDir)
	})
	// both backups were moved into place: the log must not send the admin after files that are gone
	t.Run("the backups were moved into place", func(t *testing.T) {
		skipIfRoot(t)
		w := newWorld(t, nil)
		w.newAnswer, w.oldDown = refuse, true
		w.onRestart = func(w *world, nth int) {
			if nth == 0 {
				_ = os.Chmod(w.plan.BinaryBackup, 0)
				_ = os.Chmod(w.plan.InitBackup, 0)
			}
		}
		if code := w.runHelper(); code != 2 {
			t.Fatalf("exit %d, log:\n%s", code, w.logBuf.String())
		}
		wantFile(t, w.plan.Binary, oldBinary)
		wantFile(t, w.plan.InitScript, oldScript)
		wantNoFile(t, w.plan.BinaryBackup)
		wantNoFile(t, w.plan.InitBackup)
		log := w.logBuf.String()
		// the notice before the swap names the commands for every file; the rollback line only those still needed
		_, log, _ = strings.Cut(log, "THE ROLLBACK FAILED")
		if !strings.Contains(log, "already back; only rc-service edelweiss restart remains") {
			t.Errorf("the log does not say that only the restart remains:\n%s", log)
		}
		for _, cmd := range manualCommands(w)[:2] {
			if strings.Contains(log, cmd) {
				t.Errorf("the log names %q, whose backup is gone:\n%s", cmd, log)
			}
		}
	})
}

func TestHelperFailsBeforeSwapChangesNothing(t *testing.T) {
	for _, c := range []struct {
		name   string
		setup  func(t *testing.T, w *world)
		reason string
		staged bool // the staged binary is still there afterwards
	}{
		{"the binary cannot be backed up", func(t *testing.T, w *world) {
			if err := os.Mkdir(w.plan.BinaryBackup, 0o755); err != nil {
				t.Fatal(err)
			}
		}, "backup_failed", false},
		{"the init script cannot be backed up", func(t *testing.T, w *world) {
			if err := os.Mkdir(w.plan.InitBackup, 0o755); err != nil {
				t.Fatal(err)
			}
		}, "backup_failed", false},
		{"the staged binary is gone", func(t *testing.T, w *world) {
			if err := os.Remove(w.plan.Staged); err != nil {
				t.Fatal(err)
			}
		}, "no_staged", false},
		{"init.new is gone", func(t *testing.T, w *world) {
			if err := os.Remove(w.plan.NewScript); err != nil {
				t.Fatal(err)
			}
		}, "bad_plan", false},
		{"init.new is no init script", func(t *testing.T, w *world) {
			writeFile(t, w.plan.NewScript, "echo hello\n", 0o600)
		}, "bad_plan", false},
		{"the plan names the binary as the staged one", func(t *testing.T, w *world) {
			p := w.plan
			p.Staged = p.Binary
			data, _ := json.Marshal(p)
			writeFile(t, w.planPath, string(data), 0o600)
		}, "bad_plan", true},
		{"the plan has a relative path", func(t *testing.T, w *world) {
			p := w.plan
			p.InitBackup = "init.edelweiss-bak"
			data, _ := json.Marshal(p)
			writeFile(t, w.planPath, string(data), 0o600)
		}, "bad_plan", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t, func(p *Plan) { p.Lbu = true })
			c.setup(t, w)
			if code := w.runHelper(); code != 1 {
				t.Fatalf("exit %d, log:\n%s", code, w.logBuf.String())
			}
			if len(w.events) != 0 {
				t.Errorf("events = %q, want none", w.events)
			}
			r := w.result(t)
			if r.Outcome != "failed" || r.Reason != c.reason || r.From != "0.3.0" || r.To != "0.3.1" || !r.At.Equal(w.now) {
				t.Errorf("result = %+v, want failed, %s", r, c.reason)
			}
			wantFile(t, w.plan.Binary, oldBinary)
			wantFile(t, w.plan.InitScript, oldScript)
			if _, err := os.Stat(w.plan.Staged); (err == nil) != c.staged {
				t.Errorf("the staged binary: %v, want there = %v", err, c.staged)
			}
			wantNoTemp(t, filepath.Dir(w.plan.Binary))
			wantNoTemp(t, filepath.Dir(w.plan.InitScript))
			if log := w.logBuf.String(); !strings.Contains(log, c.reason) {
				t.Errorf("log:\n%s", log)
			}
		})
	}
}

// Without the lock another update may be at work: the helper logs, and writes nothing, not even a result or helper.log.
func TestHelperWithoutTheLockWritesNothing(t *testing.T) {
	w := newWorld(t, nil)
	earlier := Result{From: "0.2.9", To: "0.3.0", Outcome: "updated", At: helperEpoch.Add(-time.Hour)}
	if err := WriteResult(w.plan.Dir, earlier); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(w.plan.Dir, "helper.log"), "the run that holds the lock\n", 0o600)
	adoptLock = func(string) (*os.File, error) { return nil, ErrNoHelperLock }
	if code := w.runHelper(); code != 1 {
		t.Fatalf("exit %d, log:\n%s", code, w.logBuf.String())
	}
	if len(w.events) != 0 {
		t.Errorf("events = %q, want none", w.events)
	}
	if st, err := ReadState(w.plan.Dir); err != nil || st.Result == nil || !reflect.DeepEqual(*st.Result, earlier) {
		t.Errorf("state = %+v, %v; want the earlier result untouched", st, err)
	}
	if got := w.helperLog(t); got != "the run that holds the lock\n" {
		t.Errorf("helper.log = %q", got)
	}
	wantFile(t, w.plan.Binary, oldBinary)
	wantFile(t, w.plan.InitScript, oldScript)
	wantFile(t, w.plan.Staged, newBinary)
	if !strings.Contains(w.logBuf.String(), "no update lock") {
		t.Errorf("log:\n%s", w.logBuf.String())
	}
}

// Without a readable plan the helper knows neither the files nor the state directory: it logs and ends.
func TestHelperWithoutAPlan(t *testing.T) {
	for _, c := range []struct{ name, content string }{
		{"not JSON", "{not json"},
		{"no directory", `{"binary":"/usr/bin/edelweissd"}`},
		{"missing", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t, nil)
			if c.content == "" {
				if err := os.Remove(w.planPath); err != nil {
					t.Fatal(err)
				}
			} else {
				writeFile(t, w.planPath, c.content, 0o600)
			}
			if code := w.runHelper(); code != 1 {
				t.Fatalf("exit %d", code)
			}
			if len(w.events) != 0 {
				t.Errorf("events = %q", w.events)
			}
			if st, err := ReadState(w.plan.Dir); err != nil || st.Result != nil {
				t.Errorf("state = %+v, %v; want no result", st, err)
			}
			wantFile(t, w.plan.Binary, oldBinary)
			wantFile(t, w.plan.Staged, newBinary)
			if !strings.Contains(w.logBuf.String(), "plan") {
				t.Errorf("log:\n%s", w.logBuf.String())
			}
		})
	}
}

func TestHelperKeepsBackups(t *testing.T) {
	stale := func(t *testing.T, w *world) {
		writeFile(t, w.plan.BinaryBackup, "edelweissd 0.2.9", 0o700)
		writeFile(t, w.plan.InitBackup, "#!/sbin/openrc-run\n# 0.2.9\n", 0o600)
	}
	t.Run("after an update", func(t *testing.T) {
		w := newWorld(t, nil)
		stale(t, w)
		if code := w.runHelper(); code != 0 {
			t.Fatalf("exit %d, log:\n%s", code, w.logBuf.String())
		}
		// the backups of an earlier update are replaced by the version that ran before this one
		wantFile(t, w.plan.BinaryBackup, oldBinary)
		wantFileMode(t, w.plan.InitBackup, oldScript, 0o644)
	})
	t.Run("after a rollback", func(t *testing.T) {
		w := newWorld(t, nil)
		stale(t, w)
		// the backup keeps the mode of the binary, and the rollback gives it back
		if err := os.Chmod(w.plan.Binary, 0o750); err != nil {
			t.Fatal(err)
		}
		w.newAnswer = refuse
		if code := w.runHelper(); code != 1 {
			t.Fatalf("exit %d, log:\n%s", code, w.logBuf.String())
		}
		// put back by a copy: the backups stay for the manual way
		wantFileMode(t, w.plan.BinaryBackup, oldBinary, 0o750)
		wantFileMode(t, w.plan.Binary, oldBinary, 0o750)
		wantFileMode(t, w.plan.InitBackup, oldScript, 0o644)
		wantFile(t, w.plan.InitScript, oldScript)
		if got, want := entries(t, filepath.Dir(w.plan.Binary)), []string{"edelweissd", "edelweissd.edelweiss-bak"}; !slices.Equal(got, want) {
			t.Errorf("bin = %q, want %q", got, want)
		}
		if got, want := entries(t, filepath.Dir(w.plan.InitScript)), []string{"edelweiss"}; !slices.Equal(got, want) {
			t.Errorf("init.d = %q, want %q", got, want)
		}
	})
}

func TestHelperEscapeFailureIsOnlyLogged(t *testing.T) {
	w := newWorld(t, nil)
	d := w.deps()
	d.Escape = filepath.Join(w.root, "cgroup", "edelweiss-selfupdate") // below a regular file: cannot be made
	if code := RunHelper(w.planPath, d); code != 0 {
		t.Fatalf("exit %d, log:\n%s", code, w.logBuf.String())
	}
	if log := w.logBuf.String(); !strings.Contains(log, "cgroup") || !strings.Contains(log, "may take the helper along") {
		t.Errorf("log:\n%s", log)
	}
	wantFile(t, w.plan.Binary, newBinary)
}

func TestHelperPanics(t *testing.T) {
	t.Run("after the swap", func(t *testing.T) {
		w := newWorld(t, nil)
		w.newAnswer = func(*world, context.Context, int) (Health, error) { panic("the probe exploded") }
		if code := w.runHelper(); code != 1 {
			t.Fatalf("exit %d, log:\n%s", code, w.logBuf.String())
		}
		wantRolledBack(t, w, "internal_error")
		wantNoFile(t, w.plan.Staged)
		if !strings.Contains(w.logBuf.String(), "the probe exploded") {
			t.Errorf("log:\n%s", w.logBuf.String())
		}
	})
	t.Run("before the swap", func(t *testing.T) {
		w := newWorld(t, nil)
		rename = func(from, to string) error {
			if from == w.plan.Staged {
				panic("the rename exploded")
			}
			return os.Rename(from, to)
		}
		if code := w.runHelper(); code != 1 {
			t.Fatalf("exit %d, log:\n%s", code, w.logBuf.String())
		}
		if len(w.events) != 0 {
			t.Errorf("events = %q, want none", w.events)
		}
		if r := w.result(t); r.Outcome != "failed" || r.Reason != "internal_error" {
			t.Errorf("result = %+v", r)
		}
		wantFile(t, w.plan.Binary, oldBinary)
		wantFile(t, w.plan.InitScript, oldScript)
		wantNoFile(t, w.plan.Staged)
		if !strings.Contains(w.logBuf.String(), "the rename exploded") {
			t.Errorf("log:\n%s", w.logBuf.String())
		}
	})
	t.Run("in the rollback", func(t *testing.T) {
		w := newWorld(t, nil)
		w.probePanic = true
		if code := w.runHelper(); code != 2 {
			t.Fatalf("exit %d, log:\n%s", code, w.logBuf.String())
		}
		if r := w.result(t); r.Outcome != "rollback_failed" || r.Reason != "internal_error" {
			t.Errorf("result = %+v", r)
		}
		wantFile(t, w.plan.Binary, oldBinary)
		wantFile(t, w.plan.InitScript, oldScript)
		wantManualCommands(t, w, w.logBuf.String())
	})
}

// levelLog records what a leveled Log (a *syslog.Writer in production) gets, line by line with the level.
type levelLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *levelLog) add(level, m string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, level+" "+m)
	return nil
}
func (l *levelLog) Info(m string) error   { return l.add("INFO", m) }
func (l *levelLog) Notice(m string) error { return l.add("NOTICE", m) }
func (l *levelLog) Err(m string) error    { return l.add("ERR", m) }
func (l *levelLog) Crit(m string) error   { return l.add("CRIT", m) }
func (l *levelLog) Write(p []byte) (int, error) {
	return len(p), l.add("WRITE", string(p))
}

func (l *levelLog) has(t *testing.T, prefix string) {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if strings.HasPrefix(line, prefix) {
			return
		}
	}
	t.Errorf("no line %q in\n%s", prefix, strings.Join(l.lines, "\n"))
}

func TestHelperLogLevels(t *testing.T) {
	run := func(t *testing.T, setup func(w *world)) *levelLog {
		w := newWorld(t, nil)
		setup(w)
		rec := &levelLog{}
		d := w.deps()
		d.Log = rec
		RunHelper(w.planPath, d)
		for _, line := range rec.lines {
			if strings.HasPrefix(line, "WRITE ") {
				t.Errorf("a line without a level: %q", line)
			}
		}
		return rec
	}
	rec := run(t, func(*world) {})
	rec.has(t, "NOTICE self-update: updating edelweissd from 0.3.0 to 0.3.1")
	rec.has(t, "NOTICE self-update: updated edelweissd from 0.3.0 to 0.3.1")
	rec = run(t, func(w *world) { w.newAnswer = refuse })
	rec.has(t, "INFO self-update: not yet: connection refused")
	rec.has(t, "ERR self-update: the update to 0.3.1 failed (not_healthy)")
	rec.has(t, "ERR self-update: rolled back to 0.3.0 (not_healthy)")
	rec = run(t, func(w *world) { w.newAnswer, w.oldDown = refuse, true })
	rec.has(t, "CRIT self-update: THE ROLLBACK FAILED (not_healthy)")
}

var helperLogLine = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ (INFO|NOTICE|ERR|CRIT) (self-update: .*)$`)

func TestHelperLogFile(t *testing.T) {
	t.Run("every line", func(t *testing.T) {
		w := newWorld(t, nil)
		w.newAnswer = refuse
		if code := w.runHelper(); code != 1 {
			t.Fatalf("exit %d, log:\n%s", code, w.logBuf.String())
		}
		if fi, err := os.Stat(filepath.Join(w.plan.Dir, "helper.log")); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("helper.log: %v, %v", fi, err)
		}
		var got []string
		for _, line := range strings.Split(strings.TrimSuffix(w.helperLog(t), "\n"), "\n") {
			m := helperLogLine.FindStringSubmatch(line)
			if m == nil {
				t.Fatalf("helper.log line %q", line)
			}
			got = append(got, m[2])
		}
		if want := strings.Split(strings.TrimSuffix(w.logBuf.String(), "\n"), "\n"); !slices.Equal(got, want) {
			t.Errorf("helper.log has\n%s\nthe log has\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	})
	t.Run("kept short", func(t *testing.T) {
		w := newWorld(t, nil)
		var old strings.Builder
		for i := 0; old.Len() < 300<<10; i++ {
			fmt.Fprintf(&old, "old line %06d\n", i)
		}
		writeFile(t, filepath.Join(w.plan.Dir, "helper.log"), old.String(), 0o644)
		if code := w.runHelper(); code != 0 {
			t.Fatalf("exit %d, log:\n%s", code, w.logBuf.String())
		}
		got := w.helperLog(t)
		if fi, _ := os.Stat(filepath.Join(w.plan.Dir, "helper.log")); fi.Mode().Perm() != 0o600 {
			t.Errorf("helper.log has mode %v", fi.Mode())
		}
		oldPart, _, _ := strings.Cut(got, "Z NOTICE self-update: updating")
		oldPart = oldPart[:strings.LastIndexByte(oldPart, '\n')+1]
		if len(oldPart) > 256<<10 || len(oldPart) < 250<<10 {
			t.Errorf("%d bytes of the old log kept, want the last 256 KiB", len(oldPart))
		}
		if !strings.HasPrefix(oldPart, "old line ") || !strings.HasSuffix(old.String(), oldPart) {
			t.Errorf("the old log was not cut at a line: %q…", oldPart[:40])
		}
		if !strings.Contains(got, "updated edelweissd from 0.3.0 to 0.3.1") {
			t.Errorf("this run is missing:\n%s", got[len(oldPart):])
		}
	})
}

// stuckLog blocks every write until release is closed, as a syslog socket whose receiver does not read.
type stuckLog struct{ release chan struct{} }

func (s stuckLog) Write(p []byte) (int, error) {
	<-s.release
	return len(p), nil
}

type panicLog struct{}

func (panicLog) Write([]byte) (int, error) { panic("the log exploded") }

func TestStuckSyslogDoesNotStallTheHelper(t *testing.T) {
	for _, c := range []struct {
		name    string
		setup   func(w *world)
		code    int
		outcome string
	}{
		{"an update", func(*world) {}, 0, "updated"},
		{"a failed rollback", func(w *world) { w.newAnswer, w.oldDown = refuse, true }, 2, "rollback_failed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t, nil)
			c.setup(w)
			stuck := stuckLog{release: make(chan struct{})}
			t.Cleanup(func() { close(stuck.release) })
			d := w.deps()
			d.Log = stuck
			done := make(chan int, 1)
			start := time.Now()
			go func() { done <- RunHelper(w.planPath, d) }()
			select {
			case code := <-done:
				if code != c.code {
					t.Errorf("exit %d, want %d", code, c.code)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the helper is stuck on its log")
			}
			if d := time.Since(start); d > 5*time.Second {
				t.Errorf("the helper took %v", d)
			}
			if r := w.result(t); r.Outcome != c.outcome {
				t.Errorf("result = %+v", r)
			}
			// the manual way is in helper.log when syslog does not take it
			if c.outcome == "rollback_failed" {
				wantManualCommands(t, w, w.helperLog(t))
			}
		})
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// While the Log is stuck, send never waits: a full queue drops lines, and their number comes with the next line.
func TestAsyncLogDropsWhenStuck(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	var got []string
	a := startAsyncLog(writerFunc(func(p []byte) (int, error) {
		<-release
		mu.Lock()
		defer mu.Unlock()
		got = append(got, string(p))
		return len(p), nil
	}))
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		for i := 0; i < logQueue+100; i++ {
			a.send(levelNotice, fmt.Sprintf("line %d", i))
		}
	}()
	select {
	case <-sent:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("send waits for the stuck log")
	}
	close(release)
	a.close(5 * time.Second)
	mu.Lock()
	defer mu.Unlock()
	lines, dropped := 0, 0
	for _, l := range got {
		// the drops may be reported in more than one line
		var n int
		if _, err := fmt.Sscanf(l, "self-update: %d log lines were dropped\n", &n); err != nil {
			lines++
		} else {
			dropped += n
		}
	}
	if dropped == 0 || lines+dropped != logQueue+100 || lines < logQueue {
		t.Errorf("%d lines written, %d reported dropped; want all %d accounted for", lines, dropped, logQueue+100)
	}
}

func TestPanickingLogDoesNotStopTheHelper(t *testing.T) {
	w := newWorld(t, nil)
	d := w.deps()
	d.Log = panicLog{}
	if code := RunHelper(w.planPath, d); code != 0 {
		t.Errorf("exit %d", code)
	}
	if r := w.result(t); r.Outcome != "updated" {
		t.Errorf("result = %+v", r)
	}
}

// runHelperChild is the test binary as the launched helper: RunHelper with the real AdoptLock on fd 3 and a fake world.
// A collection before each command must not release the lock, so RunHelper must keep the adopted file referenced.
func runHelperChild(planPath string) int {
	var p Plan
	if data, err := os.ReadFile(planPath); err == nil {
		_ = json.Unmarshal(data, &p)
	}
	w := &world{root: os.Getenv(testDirVar), plan: p, now: helperEpoch, runs: map[string]int{}}
	w.log = &w.logBuf
	w.beforeRun = func() {
		runtime.GC()
		runtime.GC()
		time.Sleep(20 * time.Millisecond) // for the finalizers
	}
	code := RunHelper(planPath, w.deps())
	lines := []string{
		"code=" + strconv.Itoa(code), "lockAtRun=" + fmt.Sprint(w.lockAtRun), "events=" + strings.Join(w.events, ","),
		"log=" + strconv.Quote(w.logBuf.String()),
	}
	report := os.Getenv(testReportVar)
	if err := os.WriteFile(report+".tmp", []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		return 3
	}
	if err := os.Rename(report+".tmp", report); err != nil {
		return 3
	}
	return code
}

func TestHelperAdoptsTheHandedOnLock(t *testing.T) {
	bin, _ := filepath.Abs(os.Args[0])
	w := newWorld(t, nil)
	report := filepath.Join(w.root, "report")
	t.Setenv(testModeVar, "runhelper")
	t.Setenv(testReportVar, report)
	t.Setenv(testDirVar, w.root)
	lock, err := AcquireLock(w.plan.Dir)
	if err != nil {
		t.Fatal(err)
	}
	done, err := ExecLauncher{Binary: bin}.Launch(w.planPath, lock)
	lock.Close()
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, done)
	got := readReport(t, report)
	if got["code"] != "0" || got["lockAtRun"] != "[true]" || got["events"] != restartCmd+",probe,sleep 30s,probe" {
		t.Errorf("the helper: %v", got)
	}
	if r := w.result(t); r.Outcome != "updated" {
		t.Errorf("result = %+v", r)
	}
	wantFile(t, w.plan.Binary, newBinary)

	// started without the lock on fd 3: refused before anything is changed, and nothing is written
	w2 := newWorld(t, nil)
	report2 := filepath.Join(w2.root, "report")
	cmd := exec.Command(bin, "self-update-apply", w2.planPath)
	cmd.Env = append(os.Environ(), testDirVar+"="+w2.root, testReportVar+"="+report2)
	var exit *exec.ExitError
	if err := cmd.Run(); !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Errorf("without the lock: %v, want exit 1", err)
	}
	if got := readReport(t, report2); got["code"] != "1" || got["events"] != "" {
		t.Errorf("the helper without the lock: %v", got)
	}
	if st, err := ReadState(w2.plan.Dir); err != nil || st.Result != nil {
		t.Errorf("state = %+v, %v; want no result", st, err)
	}
	wantNoFile(t, filepath.Join(w2.plan.Dir, "helper.log"))
	wantFile(t, w2.plan.Binary, oldBinary)
	wantFile(t, w2.plan.Staged, newBinary)
}

const goodHealth = `{"status":"ok","version":"0.3.1","started":"2026-11-05T10:00:00.123456789Z"}`

func healthServer(t *testing.T, tls bool, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	if tls {
		srv.StartTLS()
	} else {
		srv.Start()
	}
	t.Cleanup(srv.Close)
	return srv
}

func TestHTTPProbe(t *testing.T) {
	var method, path string
	srv := healthServer(t, false, func(rw http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		_, _ = io.WriteString(rw, goodHealth)
	})
	h, err := HTTPProbe(context.Background(), srv.URL+"/api/health", false)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 11, 5, 10, 0, 0, 123456789, time.UTC); h.Version != "0.3.1" || !h.Started.Equal(want) {
		t.Errorf("Health = %+v", h)
	}
	if method != http.MethodGet || path != "/api/health" {
		t.Errorf("request %s %s", method, path)
	}
}

func TestHTTPProbeRefusesBadAnswers(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		body   string
	}{
		{"a server error", http.StatusServiceUnavailable, goodHealth},
		{"a redirect", http.StatusFound, goodHealth},
		{"not JSON", http.StatusOK, "<html>edelweiss</html>"},
		{"no version", http.StatusOK, `{"status":"ok","started":"2026-11-05T10:00:00Z"}`},
		{"no start time", http.StatusOK, `{"status":"ok","version":"0.3.1"}`},
		{"a bad start time", http.StatusOK, `{"status":"ok","version":"0.3.1","started":"yesterday"}`},
		{"not ok", http.StatusOK, `{"status":"starting","version":"0.3.1","started":"2026-11-05T10:00:00Z"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := healthServer(t, false, func(rw http.ResponseWriter, r *http.Request) {
				if c.status == http.StatusFound {
					rw.Header().Set("Location", "/elsewhere")
				}
				rw.WriteHeader(c.status)
				_, _ = io.WriteString(rw, c.body)
			})
			if h, err := HTTPProbe(context.Background(), srv.URL+"/api/health", false); err == nil {
				t.Errorf("HTTPProbe = %+v, want an error", h)
			}
		})
	}
	srv := healthServer(t, false, func(http.ResponseWriter, *http.Request) {})
	url := srv.URL
	srv.Close()
	if _, err := HTTPProbe(context.Background(), url+"/api/health", false); err == nil {
		t.Error("a closed server answered")
	}
}

func TestHTTPProbeTLS(t *testing.T) {
	srv := healthServer(t, true, func(rw http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(rw, goodHealth) })
	if h, err := HTTPProbe(context.Background(), srv.URL+"/api/health", true); err != nil || h.Version != "0.3.1" {
		t.Errorf("insecure: %+v, %v", h, err)
	}
	if _, err := HTTPProbe(context.Background(), srv.URL+"/api/health", false); err == nil {
		t.Error("an unknown certificate was accepted without insecure")
	}
}

func TestHTTPProbeGivesUpAfterThreeSeconds(t *testing.T) {
	release := make(chan struct{})
	srv := healthServer(t, false, func(rw http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	defer close(release) // before the server closes, which waits for the handler
	start := time.Now()
	if _, err := HTTPProbe(context.Background(), srv.URL+"/api/health", false); err == nil {
		t.Error("a hung server answered")
	}
	if d := time.Since(start); d < 2500*time.Millisecond || d > 4500*time.Millisecond {
		t.Errorf("gave up after %v, want 3 s", d)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start = time.Now()
	if _, err := HTTPProbe(ctx, srv.URL+"/api/health", false); err == nil {
		t.Error("a hung server answered")
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("a shorter deadline of the caller: gave up after %v", d)
	}
}

func TestProductionHelperDeps(t *testing.T) {
	d := ProductionHelperDeps()
	if c, ok := d.Log.(io.Closer); ok && d.Log != io.Writer(os.Stderr) {
		defer c.Close()
	}
	same := func(a, b any) bool { return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer() }
	if d.Run == nil || !same(d.Run, ExecRun) || d.Probe == nil || !same(d.Probe, HTTPProbe) {
		t.Error("Run or Probe is not the production one")
	}
	if d.Now == nil || !same(d.Now, time.Now) || d.Sleep == nil || !same(d.Sleep, time.Sleep) || d.Log == nil {
		t.Error("Now, Sleep or Log is missing")
	}
	if d.Cgroup != "/proc/self/cgroup" || d.Escape != "/sys/fs/cgroup/edelweiss-selfupdate" {
		t.Errorf("Cgroup = %q, Escape = %q", d.Cgroup, d.Escape)
	}
}
