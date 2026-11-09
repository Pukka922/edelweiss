package selfupdate

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/syslog"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"
)

// Health is what /api/health says: the version and the start time of the process that answers.
type Health struct {
	Version string
	Started time.Time
}

// HelperDeps is what RunHelper does to the world besides its files: ProductionHelperDeps in production, fakes in tests.
type HelperDeps struct {
	Run   Runner
	Probe func(ctx context.Context, url string, insecure bool) (Health, error) // 3 s per probe
	Now   func() time.Time
	Sleep func(time.Duration)
	// Log gets every line of the helper, at its level when it has the leveled methods of a *syslog.Writer. RunHelper
	// writes to it from a goroutine of its own and drops lines rather than wait, so a Log that blocks (a syslog socket
	// whose queue is full) never stalls the helper. Every line also goes to <Dir>/helper.log.
	Log    io.Writer // the syslog writer "edelweiss" in production
	Cgroup string    // "/proc/self/cgroup" (tests: a temp file)
	Escape string    // "/sys/fs/cgroup/edelweiss-selfupdate"
}

// The outcomes of an update in result.json, and the reasons the page maps to texts.
const (
	outcomeUpdated        = "updated"
	outcomeRolledBack     = "rolled_back"
	outcomeRollbackFailed = "rollback_failed"
	outcomeFailed         = "failed"

	// rolled_back and rollback_failed: why the new version was given up.
	reasonNotHealthy    = "not_healthy"    // it did not report its version within Wait
	reasonUnstable      = "unstable"       // Stable later it did not answer as the same process
	reasonRestartFailed = "restart_failed" // rc-service edelweiss restart failed
	// swap_failed: the new binary or init script could not be put in place. With failed: the very first rename, so
	// nothing was replaced.
	reasonSwapFailed = "swap_failed"
	// internal_error: the helper panicked; before the swap with failed, after it with a rollback.
	reasonInternal = "internal_error"

	// failed: the helper stopped before it replaced the binary or the init script.
	reasonBadPlan      = "bad_plan"      // plan.json or init.new is not usable
	reasonNoStaged     = "no_staged"     // the staged binary is not there
	reasonBackupFailed = "backup_failed" // a backup could not be made
)

const (
	probeTimeout   = 3 * time.Second
	probeInterval  = time.Second
	rollbackWait   = 60 * time.Second // for the old version after a rollback
	commandTimeout = 2 * time.Minute  // rc-service edelweiss restart, lbu commit
	maxHealthBody  = 64 << 10

	helperLogFile = "helper.log"
	maxHelperLog  = 256 << 10 // what helper.log keeps of earlier runs
	logQueue      = 256       // lines waiting for Log before one is dropped
	logDrain      = time.Second
	initMode      = 0o755 // of the init script
	initBackMode  = 0o644 // of its backup: never mistaken for a script to run
)

// adoptLock and rename are AdoptLock and os.Rename; variables for the tests, which run in one process and have no lock
// on fd 3, and make a rename fail or panic on cue.
var (
	adoptLock = AdoptLock
	rename    = os.Rename
)

// ProductionHelperDeps are the dependencies of the real helper: ExecRun, HTTPProbe, the real clock and the syslog
// (tag edelweiss, facility daemon), or stderr when there is no syslog, which in the helper is the null device. The
// copy of the log in helper.log holds what syslog does not get.
func ProductionHelperDeps() HelperDeps {
	var log io.Writer = os.Stderr
	if w, err := syslog.New(syslog.LOG_DAEMON|syslog.LOG_NOTICE, "edelweiss"); err == nil {
		log = w
	}
	return HelperDeps{
		Run: ExecRun, Probe: HTTPProbe, Now: time.Now, Sleep: time.Sleep, Log: log,
		Cgroup: procCgroup, Escape: escapeDir,
	}
}

// HTTPProbe asks url (the /api/health of edelweissd) once, within 3 s and the deadline of ctx. The certificate is only
// left unchecked when insecure. Anything but a 200 with status ok, a version and an RFC 3339 start time is an error.
// It uses no proxy, no redirect and no kept-alive connection: each probe dials afresh, as the server restarts.
func HTTPProbe(ctx context.Context, url string, insecure bool) (Health, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	tr := &http.Transport{
		Proxy:             nil,
		DisableKeepAlives: true,
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: insecure}, //nolint:gosec // the server's own, usually self-signed certificate
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{
		Transport:     tr,
		Timeout:       probeTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Health{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return Health{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Health{}, fmt.Errorf("%s: status %d", url, resp.StatusCode)
	}
	var body struct {
		Status  string `json:"status"`
		Version string `json:"version"`
		Started string `json:"started"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxHealthBody)).Decode(&body); err != nil {
		return Health{}, fmt.Errorf("%s: %w", url, err)
	}
	if body.Status != "ok" || body.Version == "" {
		return Health{}, fmt.Errorf("%s: status %q, version %q", url, body.Status, body.Version)
	}
	started, err := time.Parse(time.RFC3339Nano, body.Started)
	if err != nil {
		return Health{}, fmt.Errorf("%s: the start time: %w", url, err)
	}
	return Health{Version: body.Version, Started: started}, nil
}

// RunHelper is the helper of a self-update, run by the old, proven binary (edelweissd self-update-apply <plan>) in a
// session of its own with the update lock as fd 3. It swaps the binary and the init script, restarts the service and
// checks that the new version answers and, Stable later, still answers as the same process. Otherwise it puts back
// what it replaced and restarts again, so that either the new or the old version runs. The rollback never runs the
// new binary, and a panic after the swap still rolls back. It returns the exit code: 0 updated, 1 rolled back or
// failed without a change, 2 the rollback failed (the log then names the commands of the manual way). Every step goes
// to d.Log and helper.log, the outcome to result.json.
func RunHelper(planPath string, d HelperDeps) (code int) {
	if d.Log == nil {
		d.Log = io.Discard
	}
	h := &helper{d: d, log: startAsyncLog(d.Log)}
	defer h.close() // after the recovery below: the lock is held until the outcome is written
	defer func() {
		if r := recover(); r != nil {
			code = h.recovered(r)
		}
	}()
	return h.run(planPath)
}

type helper struct {
	d    HelperDeps
	log  *asyncLog
	file *os.File // <Dir>/helper.log, from when the lock is held
	held []string // the lines before that
	lock *os.File // also keeps the adopted file referenced: its finalizer would release the lock
	p    Plan

	checked       bool        // the plan passed check: its paths are trusted
	binaryMode    fs.FileMode // of the binary before the update
	binarySwapped bool        // the new binary replaced the old one
	initSwapped   bool        // the new init script replaced the old one
	binaryMoved   bool        // the rollback moved the binary backup into place, as it could not copy it
	initMoved     bool        // the same for the init script
	rollingBack   bool
	reason        string // of the rollback under way
}

func (h *helper) run(planPath string) int {
	// Out of the cgroup of the service first, so that stopping edelweiss does not stop the helper. Best effort.
	if err := escapeCgroup(h.d.Cgroup, h.d.Escape, os.Getpid()); err != nil {
		h.errf("could not leave the cgroup %s, a restart of edelweiss may take the helper along: %v", serviceCgroup, err)
	}

	p, err := readPlan(planPath)
	if err != nil {
		h.errf("cannot use the plan %s, nothing was changed: %v", planPath, err)
		return 1
	}
	h.p = p
	h.notice("updating edelweissd from %s to %s (plan %s)", p.From, p.To, planPath)

	// The lock the launcher took and handed on. Without it another update may be at work: change nothing and write
	// nothing, not the result and not helper.log.
	lock, err := adoptLock(p.Dir)
	if err != nil {
		h.errf("no update lock, nothing was changed: %v", err)
		return 1
	}
	h.lock = lock
	h.openLog()

	if err := p.check(); err != nil {
		h.errf("the plan is not usable: %v", err)
		return h.failed(reasonBadPlan)
	}
	h.checked = true // from here on the staged binary is removed when the update fails without a change
	script, err := os.ReadFile(p.NewScript)
	if err == nil && !bytes.HasPrefix(script, []byte("#!/sbin/openrc-run")) {
		err = errors.New("it is not an openrc-run script")
	}
	if err != nil {
		h.errf("the new init script %s is not usable: %v", p.NewScript, err)
		return h.failed(reasonBadPlan)
	}
	fi, err := os.Lstat(p.Staged)
	if err == nil && !fi.Mode().IsRegular() {
		err = errors.New("it is not a regular file")
	}
	if err != nil {
		h.errf("the staged binary %s is not there: %v", p.Staged, err)
		return h.failed(reasonNoStaged)
	}

	// The backups, each a whole copy or none: the binary's with its mode, the init script's not executable.
	h.notice("backing up %s to %s and %s to %s", p.Binary, p.BinaryBackup, p.InitScript, p.InitBackup)
	fi, err = os.Stat(p.Binary)
	if err == nil {
		h.binaryMode = fi.Mode().Perm()
		err = copyFile(p.Binary, p.BinaryBackup, h.binaryMode)
	}
	if err != nil {
		h.errf("cannot back up the binary: %v", err)
		return h.failed(reasonBackupFailed)
	}
	if err := copyFile(p.InitScript, p.InitBackup, initBackMode); err != nil {
		h.errf("cannot back up the init script: %v", err)
		return h.failed(reasonBackupFailed)
	}

	// For an update that is cut short (the host loses power, the helper is killed): the way back by hand, as README says.
	h.notice("if this update is interrupted, bring the old version back by hand: mv %s %s; install -m %o %s %s; rc-service edelweiss restart",
		p.BinaryBackup, p.Binary, initMode, p.InitBackup, p.InitScript)
	h.notice("putting %s in place of %s", p.Staged, p.Binary)
	if err := rename(p.Staged, p.Binary); err != nil {
		h.errf("cannot put the new binary in place: %v", err)
		return h.failed(reasonSwapFailed) // nothing was replaced
	}
	h.binarySwapped = true
	// From here on every failure rolls back what was replaced.
	if err := syncDir(filepath.Dir(p.Binary)); err != nil {
		return h.rollBack(reasonSwapFailed, err)
	}
	h.notice("writing the init script of %s to %s", p.To, p.InitScript)
	tmp, err := stageFile(p.InitScript, bytes.NewReader(script), initMode)
	if err != nil {
		return h.rollBack(reasonSwapFailed, err)
	}
	if err := rename(tmp, p.InitScript); err != nil {
		_ = os.Remove(tmp)
		return h.rollBack(reasonSwapFailed, err)
	}
	h.initSwapped = true
	if err := syncDir(filepath.Dir(p.InitScript)); err != nil {
		return h.rollBack(reasonSwapFailed, err)
	}

	if err := h.command(p.RcService, "edelweiss", "restart"); err != nil {
		return h.rollBack(reasonRestartFailed, err)
	}
	first, err := h.waitFor(p.To, p.Wait)
	if err != nil {
		return h.rollBack(reasonNotHealthy, err)
	}
	h.notice("checking again in %v that %s still runs as the same process", p.Stable, p.To)
	h.d.Sleep(p.Stable)
	again, err := h.probe(probeTimeout)
	switch {
	case err != nil:
		return h.rollBack(reasonUnstable, err)
	case again.Version != p.To:
		return h.rollBack(reasonUnstable, fmt.Errorf("version %q answers", again.Version))
	case !again.Started.Equal(first.Started):
		return h.rollBack(reasonUnstable, fmt.Errorf("it started again at %s, after %s", again.Started.Format(time.RFC3339Nano), first.Started.Format(time.RFC3339Nano)))
	}

	var lbuErr string
	if p.Lbu {
		if err := h.command(p.LbuTool, "commit"); err != nil {
			lbuErr = err.Error()
			h.errf("the update stays, but does not survive a reboot of the host")
		}
	}
	h.finish(outcomeUpdated, "", lbuErr)
	h.notice("updated edelweissd from %s to %s", p.From, p.To)
	return 0
}

// finish writes result.json; a failure is only logged, as nothing else can be done about it.
func (h *helper) finish(outcome, reason, lbuErr string) {
	r := Result{From: h.p.From, To: h.p.To, Outcome: outcome, Reason: reason, LbuError: lbuErr, At: h.d.Now().UTC()}
	if err := WriteResult(h.p.Dir, r); err != nil {
		h.errf("cannot write the result: %v", err)
	}
}

// failed ends an update that replaced nothing: the binary and the init script are as they were. The staged binary is
// removed once the plan is known to be sound; before that its path is not trusted.
func (h *helper) failed(reason string) int {
	if h.checked {
		if err := os.Remove(h.p.Staged); err != nil && !errors.Is(err, fs.ErrNotExist) {
			h.errf("cannot remove %s: %v", h.p.Staged, err)
		}
	}
	h.finish(outcomeFailed, reason, "")
	h.errf("the update to %s failed (%s); the binary and the init script are as they were, %s still runs", h.p.To, reason, h.p.From)
	return 1
}

// rollBack puts back what was replaced (restore), removes what is left of the staged binary, restarts edelweiss and
// waits for the old version. It runs only in this process and never runs the new binary. Only when the old version
// answers and the files are back is it rolled back; otherwise the log names the commands of the manual way.
func (h *helper) rollBack(reason string, cause error) int {
	h.rollingBack, h.reason = true, reason
	p := h.p
	h.errf("the update to %s failed (%s): %v; rolling back to %s", p.To, reason, cause, p.From)
	restored := true
	if h.binarySwapped {
		moved, err := h.restore(p.BinaryBackup, p.Binary, h.binaryMode)
		h.binaryMoved = moved
		if err != nil {
			h.errf("cannot put the binary back: %v", err)
			restored = false
		}
	}
	if h.initSwapped {
		moved, err := h.restore(p.InitBackup, p.InitScript, initMode)
		h.initMoved = moved
		if err != nil {
			h.errf("cannot put the init script back: %v", err)
			restored = false
		}
	}
	if err := os.Remove(p.Staged); err != nil && !errors.Is(err, fs.ErrNotExist) {
		h.errf("cannot remove %s: %v", p.Staged, err)
	}
	// a failed restart may still have started the old version: the probe decides
	_ = h.command(p.RcService, "edelweiss", "restart")
	_, err := h.waitFor(p.From, rollbackWait)
	if err == nil && restored {
		h.finish(outcomeRolledBack, reason, "")
		h.errf("rolled back to %s (%s)", p.From, reason)
		return 1
	}
	if err == nil {
		return h.rollbackFailed(fmt.Sprintf("edelweissd %s runs again, but not all of its files are back", p.From))
	}
	return h.rollbackFailed(fmt.Sprintf("edelweissd %s is not back: %v", p.From, err))
}

// rollbackFailed records the failed rollback, how things stand, and the manual way: the commands for the files the
// helper replaced and did not move back, and the restart.
func (h *helper) rollbackFailed(state string) int {
	p := h.p
	h.finish(outcomeRollbackFailed, h.reason, "")
	var back, steps []string
	switch {
	case h.binaryMoved:
		back = append(back, "the binary")
	case h.binarySwapped:
		steps = append(steps, "mv "+p.BinaryBackup+" "+p.Binary)
	}
	switch {
	case h.initMoved:
		back = append(back, "the init script")
	case h.initSwapped:
		steps = append(steps, fmt.Sprintf("install -m %o %s %s", initMode, p.InitBackup, p.InitScript))
	}
	steps = append(steps, "rc-service edelweiss restart")
	hand := "To bring it back by hand: " + strings.Join(steps, "; ")
	if len(back) > 0 {
		verb := " is"
		if len(back) > 1 {
			verb = " are"
		}
		already := strings.Join(back, " and ") + verb + " already back"
		already = strings.ToUpper(already[:1]) + already[1:]
		if len(steps) == 1 {
			hand = already + "; only " + steps[0] + " remains"
		} else {
			hand = already + "; to bring the rest back by hand: " + strings.Join(steps, "; ")
		}
	}
	h.crit("THE ROLLBACK FAILED (%s): %s. %s", h.reason, state, hand)
	return 2
}

// restore puts backup back over target (mode) by a copy, so that the backup stays for the manual way. When the copy
// fails – on a full tmpfs it needs room for one more binary – the backup itself is moved into place (moved): the old
// version running matters more than its backup.
func (h *helper) restore(backup, target string, mode fs.FileMode) (moved bool, err error) {
	err = copyFile(backup, target, mode)
	if err == nil {
		return false, nil
	}
	h.errf("cannot copy %s to %s (%v); moving it there instead, the backup is then gone", backup, target, err)
	if err := rename(backup, target); err != nil {
		return false, err
	}
	if err := os.Chmod(target, mode); err != nil {
		return true, err
	}
	return true, syncDir(filepath.Dir(target))
}

// recovered turns a panic into an outcome: before the swap a failure without a change, after it a rollback, in the
// rollback a failed rollback. A panic while it does that ends in a failed rollback, once the swap happened.
func (h *helper) recovered(r any) (code int) {
	h.crit("the helper failed: panic: %v", r)
	h.info("%s", debug.Stack())
	unknown := func(r any) string {
		return fmt.Sprintf("the helper panicked (%v), whether edelweissd %s is back is unknown", r, h.p.From)
	}
	defer func() {
		if r := recover(); r != nil {
			h.crit("the helper failed again: panic: %v", r)
			code = 1
			if h.binarySwapped {
				code = h.rollbackFailed(unknown(r))
			}
		}
	}()
	switch {
	case h.rollingBack:
		return h.rollbackFailed(unknown(r))
	case h.binarySwapped:
		return h.rollBack(reasonInternal, fmt.Errorf("panic: %v", r))
	case h.lock != nil:
		return h.failed(reasonInternal)
	}
	return 1
}

// close ends the helper: helper.log is synced, the log lines are handed on (for at most logDrain), and the lock goes
// last.
func (h *helper) close() {
	if h.file != nil {
		_ = h.file.Sync()
		_ = h.file.Close()
	}
	h.log.close(logDrain)
	if h.lock != nil {
		_ = h.lock.Close()
	}
}

// command runs name with the timeout of the service commands.
func (h *helper) command(name string, args ...string) error {
	line := strings.Join(append([]string{name}, args...), " ")
	h.notice("running %s", line)
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	if _, err := h.d.Run(ctx, name, args...); err != nil {
		h.errf("%s failed: %v", line, err)
		return err
	}
	return nil
}

// probe asks the health endpoint once, within timeout.
func (h *helper) probe(timeout time.Duration) (Health, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return h.d.Probe(ctx, h.p.HealthURL, h.p.Insecure)
}

// waitFor probes every second until version answers, for up to wait. A probe takes at most 3 s and never runs past
// the deadline, so that a hung connection cannot use up the wait. A changed error is logged once; the caller logs the
// final one.
func (h *helper) waitFor(version string, wait time.Duration) (Health, error) {
	h.notice("waiting up to %v for %s at %s", wait, version, h.p.HealthURL)
	deadline := h.d.Now().Add(wait)
	last := errors.New("no answer")
	lastMsg := ""
	for {
		left := deadline.Sub(h.d.Now())
		if left <= 0 {
			break
		}
		hl, err := h.probe(min(probeTimeout, left))
		switch {
		case err == nil && hl.Version == version:
			h.notice("%s answers, started %s", version, hl.Started.Format(time.RFC3339Nano))
			return hl, nil
		case err == nil:
			last = fmt.Errorf("version %q answers", hl.Version)
		default:
			last = err
		}
		if msg := last.Error(); msg != lastMsg {
			h.info("not yet: %s", msg)
			lastMsg = msg
		}
		left = deadline.Sub(h.d.Now())
		if left <= 0 {
			break
		}
		h.d.Sleep(min(probeInterval, left))
	}
	return Health{}, fmt.Errorf("%s did not answer within %v: %w", version, wait, last)
}

// level is the syslog severity of a line of the helper.
type level int

const (
	levelInfo level = iota
	levelNotice
	levelErr
	levelCrit
)

func (l level) String() string { return [...]string{"INFO", "NOTICE", "ERR", "CRIT"}[l] }

func (h *helper) info(format string, args ...any)   { h.logAt(levelInfo, format, args...) }
func (h *helper) notice(format string, args ...any) { h.logAt(levelNotice, format, args...) }
func (h *helper) errf(format string, args ...any)   { h.logAt(levelErr, format, args...) }
func (h *helper) crit(format string, args ...any)   { h.logAt(levelCrit, format, args...) }

// logAt hands a line to Log (never waiting for it) and appends it to helper.log, or keeps it for helper.log until
// the lock is held.
func (h *helper) logAt(lvl level, format string, args ...any) {
	msg := "self-update: " + fmt.Sprintf(format, args...)
	h.log.send(lvl, msg)
	line := h.d.Now().UTC().Format(time.RFC3339) + " " + lvl.String() + " " + msg + "\n"
	if h.file != nil {
		_, _ = h.file.WriteString(line)
	} else {
		h.held = append(h.held, line)
	}
}

// openLog starts helper.log in the plan directory (0600), trimmed to its last maxHelperLog bytes, with the lines so
// far. A helper.log that cannot be opened only costs the copy of the log.
func (h *helper) openLog() {
	path := filepath.Join(h.p.Dir, helperLogFile)
	err := trimLog(path, maxHelperLog)
	var f *os.File
	if err == nil {
		f, err = os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	}
	if err == nil {
		if err = f.Chmod(0o600); err != nil {
			f.Close()
		}
	}
	if err != nil {
		h.held = nil
		h.errf("cannot write %s: %v", path, err)
		return
	}
	for _, line := range h.held {
		_, _ = f.WriteString(line)
	}
	h.held, h.file = nil, f
}

// trimLog keeps the last max bytes of the file path, from the start of a line, when it is longer. A file that is not
// there is no error.
func trimLog(path string, max int64) error {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() <= max {
		return err
	}
	tail := make([]byte, max)
	if _, err := f.ReadAt(tail, fi.Size()-max); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if i := bytes.IndexByte(tail, '\n'); i >= 0 {
		tail = tail[i+1:]
	}
	return writeFileAtomic(path, tail)
}

// leveledLog is the part of *syslog.Writer that writes at a severity.
type leveledLog interface {
	Info(string) error
	Notice(string) error
	Err(string) error
	Crit(string) error
}

// asyncLog hands lines to a Log in a goroutine of its own. When its queue is full a line is dropped and counted, and
// the count is reported with the next line that gets through. A Log that panics is not used again.
type asyncLog struct {
	lines   chan logLine
	done    chan struct{}
	dropped atomic.Int64
}

type logLine struct {
	lvl level
	msg string
}

func startAsyncLog(w io.Writer) *asyncLog {
	a := &asyncLog{lines: make(chan logLine, logQueue), done: make(chan struct{})}
	go a.run(w)
	return a
}

func (a *asyncLog) run(w io.Writer) {
	defer close(a.done)
	ok := true
	for l := range a.lines {
		if n := a.dropped.Swap(0); n > 0 && ok {
			ok = emit(w, levelErr, fmt.Sprintf("self-update: %d log lines were dropped", n))
		}
		if ok {
			ok = emit(w, l.lvl, l.msg)
		}
	}
}

// emit writes one line to w; false when w panicked.
func emit(w io.Writer, lvl level, msg string) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	if lw, leveled := w.(leveledLog); leveled {
		switch lvl {
		case levelInfo:
			_ = lw.Info(msg)
		case levelNotice:
			_ = lw.Notice(msg)
		case levelErr:
			_ = lw.Err(msg)
		default:
			_ = lw.Crit(msg)
		}
		return true
	}
	_, _ = io.WriteString(w, msg+"\n")
	return true
}

// send queues a line, or drops it when the queue is full; it never waits.
func (a *asyncLog) send(lvl level, msg string) {
	select {
	case a.lines <- logLine{lvl, msg}:
	default:
		a.dropped.Add(1)
	}
}

// close lets the goroutine hand on what is queued, for at most wait. A Log that blocks keeps its goroutine, which ends
// with the process. Nothing may be sent after close.
func (a *asyncLog) close(wait time.Duration) {
	close(a.lines)
	select {
	case <-a.done:
	case <-time.After(wait):
	}
}

// readPlan reads plan.json. A plan without an absolute directory is an error: the helper could neither check the
// lock nor write the result.
func readPlan(path string) (Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Plan{}, err
	}
	var p Plan
	if err := json.Unmarshal(data, &p); err != nil {
		return Plan{}, err
	}
	if !filepath.IsAbs(p.Dir) {
		return Plan{}, fmt.Errorf("the directory %q is not absolute", p.Dir)
	}
	return p, nil
}

// check makes sure the helper only touches the files it means: absolute paths, all different, the versions, the URL
// and the times set. The init backup may be anywhere, the plan directory included.
func (p Plan) check() error {
	files := []struct{ name, path string }{
		{"binary", p.Binary}, {"staged", p.Staged}, {"binaryBackup", p.BinaryBackup},
		{"initScript", p.InitScript}, {"initBackup", p.InitBackup}, {"newScript", p.NewScript},
	}
	seen := map[string]string{}
	for _, f := range files {
		if !filepath.IsAbs(f.path) {
			return fmt.Errorf("%s %q is not an absolute path", f.name, f.path)
		}
		clean := filepath.Clean(f.path)
		if other, dup := seen[clean]; dup {
			return fmt.Errorf("%s and %s are both %s", other, f.name, clean)
		}
		seen[clean] = f.name
	}
	if !filepath.IsAbs(p.RcService) {
		return fmt.Errorf("rcService %q is not an absolute path", p.RcService)
	}
	if p.Lbu && !filepath.IsAbs(p.LbuTool) {
		return fmt.Errorf("lbuTool %q is not an absolute path", p.LbuTool)
	}
	if p.From == "" || p.To == "" || p.From == p.To {
		return fmt.Errorf("from %q to %q is no update", p.From, p.To)
	}
	if p.HealthURL == "" {
		return errors.New("no health URL")
	}
	if p.Wait <= 0 || p.Stable <= 0 {
		return fmt.Errorf("wait %v and stable %v must be positive", p.Wait, p.Stable)
	}
	return nil
}

// copyFile copies the regular file src over dst (mode) through replaceFile; src stays as it is.
func copyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", src)
	}
	return replaceFile(dst, in, mode)
}

// stageFile writes r to a new temp file next to path, .<name>-<digits> (mode), and syncs it; it returns the temp
// file's path. On an error nothing is left.
func stageFile(path string, r io.Reader, mode fs.FileMode) (string, error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	_, err = io.Copy(tmp, r)
	if err == nil {
		err = tmp.Chmod(mode)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

// replaceFile writes r over path through stageFile and a rename, then syncs the directory: a crash leaves the old file
// or the whole new one, never a part. On an error the temp file is removed.
func replaceFile(path string, r io.Reader, mode fs.FileMode) error {
	tmp, err := stageFile(path, r, mode)
	if err != nil {
		return err
	}
	if err := rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return syncDir(filepath.Dir(path))
}
