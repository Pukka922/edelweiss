package selfupdate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Plan is everything the helper needs: it is written to plan.json before the helper starts, and the helper reads
// nothing else.
type Plan struct {
	Binary       string        `json:"binary"`
	Staged       string        `json:"staged"`
	BinaryBackup string        `json:"binaryBackup"` // <binary>.edelweiss-bak, with the binary's mode
	InitScript   string        `json:"initScript"`
	InitBackup   string        `json:"initBackup"` // <Dir>/init.edelweiss-bak, 0644: in /etc/init.d OpenRC would list it as a service
	NewScript    string        `json:"newScript"`  // path of init.new
	From         string        `json:"from"`       // bare versions, as /api/health reports them
	To           string        `json:"to"`
	HealthURL    string        `json:"healthUrl"`
	Insecure     bool          `json:"insecure"`
	Lbu          bool          `json:"lbu"` // the mode is diskless or data
	RcService    string        `json:"rcService"`
	LbuTool      string        `json:"lbuTool"`
	Wait         time.Duration `json:"wait"`   // for the new version to answer: 60 s
	Stable       time.Duration `json:"stable"` // before it is asked a second time: 30 s
	Dir          string        `json:"dir"`    // <state_dir>/self-update

	// Script is the content of init.new. WritePlan writes it to NewScript; it is not part of plan.json.
	Script string `json:"-"`
}

const (
	planFile   = "plan.json"
	resultFile = "result.json"
	lockFile   = "lock"
)

// HealthURL is where the helper asks whether edelweissd is up, from the listen address of the server: the addresses
// that mean "everywhere" become the loopback address. With TLS the scheme is https, and the certificate is not
// checked (insecure), as the server's is usually self-signed and the name is the loopback one.
// A listen address without a port is an error, so the helper never gets a URL it cannot use.
func HealthURL(listen string, tls bool) (healthURL string, insecure bool, err error) {
	scheme := "http"
	if tls {
		scheme, insecure = "https", true
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", false, fmt.Errorf("the listen address %q: %w", listen, err)
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return "", false, fmt.Errorf("the listen address %q has no usable port", listen)
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	if strings.ContainsAny(host, " \t/?#") {
		return "", false, fmt.Errorf("the listen address %q has no usable host", listen)
	}
	u := url.URL{Scheme: scheme, Host: net.JoinHostPort(host, port), Path: "/api/health"} // escapes an IPv6 zone
	return u.String(), insecure, nil
}

// WritePlan makes p.Dir (0700) and writes plan.json and init.new (the script of p) into it, both 0600. It returns the
// path of plan.json.
func WritePlan(p Plan) (path string, err error) {
	if p.Dir == "" {
		return "", errors.New("selfupdate: the plan has no directory")
	}
	if p.Script == "" || p.NewScript == "" {
		return "", errors.New("selfupdate: the plan has no init script")
	}
	if p.NewScript != filepath.Join(p.Dir, "init.new") {
		return "", fmt.Errorf("selfupdate: init.new must be %s, not %s", filepath.Join(p.Dir, "init.new"), p.NewScript)
	}
	if err := ensureDir(p.Dir); err != nil {
		return "", err
	}
	if err := writeFileAtomic(p.NewScript, []byte(p.Script)); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "", err
	}
	path = filepath.Join(p.Dir, planFile)
	if err := writeFileAtomic(path, append(data, '\n')); err != nil {
		return "", err
	}
	return path, nil
}

// ensureDir makes dir (0700) and tightens one that is there already to 0700.
func ensureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

// writeFileAtomic writes data to a 0600 file next to path, syncs it and renames it into place, then syncs the
// directory (replaceFile).
func writeFileAtomic(path string, data []byte) error {
	return replaceFile(path, bytes.NewReader(data), 0o600)
}

// syncDir makes the entries of dir durable. A file system that cannot sync a directory (EINVAL) is not an error.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) {
		return err
	}
	return nil
}

// Result is how the last update ended.
type Result struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Outcome  string `json:"outcome"` // "updated" | "rolled_back" | "rollback_failed" | "failed"
	Reason   string `json:"reason,omitempty"`
	LbuError string `json:"lbuError,omitempty"`
	// LbuFailed is set by the server in what it shows: lbu did not save the update (LbuError is not empty), also for those
	// who may not see the raw message. The helper does not write it.
	LbuFailed bool      `json:"lbuFailed,omitempty"`
	At        time.Time `json:"at"`
}

// State is what the page needs to know: whether a helper is working, and how the last update ended.
type State struct {
	Running bool    `json:"running"`
	Result  *Result `json:"result,omitempty"`
}

// ReadState looks into dir: Running when another process holds the lock file, Result when result.json is there. A
// directory that does not exist is an idle state.
func ReadState(dir string) (State, error) {
	var st State
	st.Running = lockHeld(filepath.Join(dir, lockFile))
	data, err := os.ReadFile(filepath.Join(dir, resultFile))
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	var r Result
	if err := json.Unmarshal(data, &r); err != nil {
		// not a hard error: the page shows it, and Dismiss removes it
		r = Result{Outcome: "failed", Reason: "result_unreadable"}
		if fi, serr := os.Stat(filepath.Join(dir, resultFile)); serr == nil {
			r.At = fi.ModTime()
		}
	}
	st.Result = &r
	return st, nil
}

// ErrHelperRunning is the error of AcquireLock when another process holds the lock.
var ErrHelperRunning = errors.New("an update is already running")

// AcquireLock takes the lock of an update: <dir>/lock (0600, in dir, which is 0700), flocked exclusively. The caller
// takes it BEFORE it launches the helper and hands it on (Launcher.Launch), so that ReadState reports Running from
// the moment the update is decided to the end of the helper, and a second update cannot start in between. Another
// process holding it gives ErrHelperRunning. The lock must be LOCK_EX: AdoptLock relies on it. A collision with the
// momentary probe of ReadState is made improbable by a short retry, not impossible: flock offers no probe that takes
// no lock. The caller closes its own file after a successful launch; the helper
// keeps the lock until it ends.
func AcquireLock(dir string) (*os.File, error) {
	if err := ensureDir(dir); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, lockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	// ReadState holds the lock for a moment while it probes: try a few times before calling the update running.
	for attempt := 0; ; attempt++ {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			break
		}
		if attempt == 2 {
			err = ErrHelperRunning
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	f.Close()
	return nil, err
}

// ErrNoHelperLock is the error of AdoptLock when fd 3 is not the update lock.
var ErrNoHelperLock = errors.New("fd 3 is not the update lock")

// AdoptLock is the helper's side of the lock: fd 3, which the launcher handed on, must be a regular file with the
// device and inode of <dir>/lock, and must be locked by us (a LOCK_EX|LOCK_NB flock on fd 3 itself succeeds when this
// open file description holds the lock, and fails when another one does). There is a third case: when nobody holds the
// lock, that flock acquires it fresh, so the check proves "we hold it from now on", not "it was handed on locked". It
// relies on AcquireLock having taken LOCK_EX: a LOCK_SH would be dropped by the EX|NB conversion, which is not atomic.
// It then marks fd 3 close-on-exec, so that no
// command the helper starts (rc-service, and the daemons below it) inherits the lock and keeps it after the helper
// ends. The helper never flocks anything else.
//
// The returned file has a finalizer that closes fd 3 and so releases the lock: the helper must keep it referenced
// until it exits (a deferred lock.Close() in the helper, or runtime.KeepAlive(lock) at its end).
func AdoptLock(dir string) (*os.File, error) {
	var fst, lst syscall.Stat_t
	if err := syscall.Fstat(3, &fst); err != nil || fst.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return nil, fmt.Errorf("%w: it is not a regular file", ErrNoHelperLock)
	}
	if err := syscall.Stat(filepath.Join(dir, lockFile), &lst); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoHelperLock, err)
	}
	if fst.Dev != lst.Dev || fst.Ino != lst.Ino {
		return nil, fmt.Errorf("%w: it is another file than %s", ErrNoHelperLock, filepath.Join(dir, lockFile))
	}
	if err := syscall.Flock(3, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, fmt.Errorf("%w: it is not locked by this process: %v", ErrNoHelperLock, err)
	}
	unix.CloseOnExec(3)
	return os.NewFile(3, filepath.Join(dir, lockFile)), nil
}

// HelperRunning tells whether a helper holds the update lock in dir (from AcquireLock to its end), also one that an
// earlier edelweissd started; the shared lock of the daemon asks it (oplock.New). Like AcquireLock it gives a collision
// with the momentary probe of ReadState a short retry: only three probes in a row, 50 ms apart, that find the lock
// held count.
func HelperRunning(dir string) bool {
	path := filepath.Join(dir, lockFile)
	for attempt := 0; ; attempt++ {
		if !lockHeld(path) {
			return false
		}
		if attempt == 2 {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// lockHeld tells whether another process holds the flock on path. A file that is not there, or cannot be opened, is
// not held. A lock this call got is released at once.
func lockHeld(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.Is(err, syscall.EWOULDBLOCK)
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

// WriteResult writes result.json (0600) in dir, which it makes if need be.
func WriteResult(dir string, r Result) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, resultFile), data)
}

// Dismiss removes result.json; there being none is no error.
func Dismiss(dir string) error {
	err := os.Remove(filepath.Join(dir, resultFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
