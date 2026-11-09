package selfupdate

// This file ties the update together for the server: what the About page shows, the cached check, and the start of
// an update, which ends with the helper taking over.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Pukka922/edelweiss/core/internal/github"
	"github.com/Pukka922/edelweiss/core/internal/oplock"
)

// Options are what a Service works with. In production: the GitHub client of the daemon, Source, runtime.GOARCH,
// the running version, os.Executable(), "/", the state directory, the listen address and TLS of the configuration,
// serve --dev, the download limit of extensions, the lock shared with the extension installer, ExecLauncher, ExecRun,
// os.Getppid().
type Options struct {
	Releases    Releases
	Source      github.Source // Source when empty
	Arch        string
	Version     string // bare, as /api/health reports it
	Binary      string // the running binary, absolute
	Root        string // where the host's files are: "/"
	StateDir    string // the update's files go to <StateDir>/self-update
	Listen      string
	TLS         bool
	Dev         bool
	MaxDownload int64
	Lock        *oplock.Lock // one install or update at a time, extensions and edelweiss itself
	Launcher    Launcher
	Run         Runner
	Logger      *slog.Logger
	Now         func() time.Time
	PPID        int
}

// About is what every signed-in user may know about the running edelweissd.
type About struct {
	Version  string `json:"version"`
	Arch     string `json:"arch"`
	Mode     Mode   `json:"mode"`
	Possible bool   `json:"possible"`
	Reason   Reason `json:"reason,omitempty"`
	Repo     string `json:"repo"` // "Pukka922/edelweiss"
	State    State  `json:"state"`
}

// CheckView is the answer of an update check: the offer, when it was made and how old it is by the clock of the core
// (the page counts on from AgeSeconds, as a browser's clock may be wrong). Stale: GitHub did not answer (rate limit,
// outage, timeout) and this is the last good check; StaleCode and StaleReason are the code and message of that
// failure. LbuWarning: the host runs from RAM and lbu does not save the binary, so the update would not survive a
// reboot.
type CheckView struct {
	Offer       Offer     `json:"offer"`
	CheckedAt   time.Time `json:"checkedAt"`
	AgeSeconds  int64     `json:"ageSeconds"`
	Stale       bool      `json:"stale,omitempty"`
	StaleCode   string    `json:"staleCode,omitempty"`
	StaleReason string    `json:"staleReason,omitempty"`
	LbuWarning  bool      `json:"lbuWarning,omitempty"`
}

var (
	ErrNotPossible = errors.New("no self-update is possible on this host")
	ErrNoUpdate    = errors.New("no newer edelweissd is available")
	ErrBusy        = errors.New("another installation or update is running")
)

// NotPossibleError is the error of Start on a host where no update is possible; it is ErrNotPossible with the reason.
type NotPossibleError struct{ Reason Reason }

func (e *NotPossibleError) Error() string { return ErrNotPossible.Error() + ": " + string(e.Reason) }
func (e *NotPossibleError) Unwrap() error { return ErrNotPossible }

const (
	// checkMaxAge is how long a check is kept: GitHub allows 60 requests an hour without a token.
	checkMaxAge = 2 * time.Minute
	// helperWait is how long the helper waits for the new version to answer, helperStable how long it waits before
	// it asks a second time.
	helperWait   = 60 * time.Second
	helperStable = 30 * time.Second
	// stateSubdir is the directory of plan.json, init.new, result.json, helper.log and the lock in the state
	// directory.
	stateSubdir = "self-update"
)

// Dir is the directory of the update's files in the state directory: <stateDir>/self-update.
func Dir(stateDir string) string { return filepath.Join(stateDir, stateSubdir) }

// acquireLock is AcquireLock; a variable for the tests.
var acquireLock = AcquireLock

// Service is the self-update of the server.
type Service struct {
	o   Options
	dir string // <StateDir>/self-update

	mu       sync.Mutex
	last     *CheckView // the last good check
	modeOnce sync.Once  // logs that the mode cannot be read
}

// New makes the Service; empty options get the production values where there are some (Source, ExecLauncher, ExecRun,
// slog.Default, time.Now, a lock of its own).
func New(o Options) *Service {
	if o.Source == (github.Source{}) {
		o.Source = Source
	}
	if o.Launcher == nil {
		o.Launcher = ExecLauncher{}
	}
	if o.Run == nil {
		o.Run = ExecRun
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Lock == nil {
		o.Lock = &oplock.Lock{}
	}
	return &Service{o: o, dir: Dir(o.StateDir)}
}

// hostFacts are what the plan needs to know about the host.
type hostFacts struct {
	mode      Mode // "" when /proc/mounts cannot be read
	tools     Tools
	healthURL string
	insecure  bool
}

// examine looks at the host and returns the first reason that rules an update out, in the order Eligible, the mode
// (unknown_mode), lbu for a host that runs from RAM (no_lbu), the listen address (listen_address); ReasonNone when an
// update is possible. About and Start ask it alike, so they agree. A mode that cannot be read is logged once.
func (s *Service) examine() (hostFacts, Reason) {
	var f hostFacts
	mode, modeErr := DetectMode(s.o.Root)
	if modeErr != nil {
		s.modeOnce.Do(func() { s.o.Logger.Warn("self-update: cannot tell the mode of the host", "err", modeErr) })
	}
	f.mode = mode
	h := Host{Root: s.o.Root, Binary: s.o.Binary, Arch: s.o.Arch, Version: s.o.Version, Dev: s.o.Dev, PPID: s.o.PPID}
	if r := Eligible(h); r != ReasonNone {
		return f, r
	}
	if modeErr != nil {
		return f, ReasonUnknownMode
	}
	f.tools = FindTools(s.o.Root)
	if f.mode != Sys && f.tools.Lbu == "" {
		return f, ReasonNoLbu
	}
	var err error
	if f.healthURL, f.insecure, err = HealthURL(s.o.Listen, s.o.TLS); err != nil {
		return f, ReasonListenAddress
	}
	return f, ReasonNone
}

// About tells the version, the mode of the host, whether an update is possible and the state of the last one. It
// does not ask GitHub.
func (s *Service) About() About {
	a := About{Version: s.o.Version, Arch: s.o.Arch, Repo: s.o.Source.Owner + "/" + s.o.Source.Repo}
	f, r := s.examine()
	a.Mode, a.Reason, a.Possible = f.mode, r, r == ReasonNone
	st, err := ReadState(s.dir)
	if err != nil {
		s.o.Logger.Warn("self-update: cannot read the state", "dir", s.dir, "err", err)
	}
	a.State = st
	return a
}

// Check asks GitHub for a newer edelweissd, or answers with the last check while it is younger than two minutes
// (refresh asks anyway). When GitHub does not answer (rate limit, outage, timeout) the last good check is the answer,
// marked stale; it stays the kept one. Any other failure is the error.
func (s *Service) Check(ctx context.Context, refresh bool) (CheckView, error) {
	s.mu.Lock()
	var last *CheckView
	if s.last != nil {
		v := *s.last
		last = &v
	}
	s.mu.Unlock()
	if !refresh && last != nil && s.o.Now().Sub(last.CheckedAt) < checkMaxAge {
		return s.view(*last), nil
	}
	off, err := Check(ctx, s.o.Releases, s.o.Source, s.o.Arch, s.o.Version)
	if err != nil {
		if code, wide := wideCode(err); wide && last != nil {
			last.Stale, last.StaleCode, last.StaleReason = true, code, err.Error()
			return s.view(*last), nil
		}
		return CheckView{}, err
	}
	return s.view(s.keep(off)), nil
}

// keep makes off the last good check.
func (s *Service) keep(off Offer) CheckView {
	v := CheckView{Offer: off, CheckedAt: s.o.Now()}
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := v
	s.last = &kept
	return v
}

// view adds what changes after a check: its age and the lbu warning.
func (s *Service) view(v CheckView) CheckView {
	v.AgeSeconds = max(0, int64(s.o.Now().Sub(v.CheckedAt)/time.Second))
	v.LbuWarning = s.lbuWarning()
	return v
}

// lbuWarning tells whether the host runs from RAM (diskless or data) and lbu does not save the binary: an unreadable
// lbu list warns too.
func (s *Service) lbuWarning() bool {
	mode, err := DetectMode(s.o.Root)
	if err != nil || mode == Sys {
		return false
	}
	in, err := LbuIncludes(s.o.Root, s.o.Binary)
	return err != nil || !in
}

// wideCode tells whether err means that GitHub did not answer at all, with the error code of the server: the context
// errors first, as a timeout of the request to GitHub is wrapped in ErrUnavailable.
func wideCode(err error) (string, bool) {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "timeout", true
	case errors.Is(err, github.ErrRateLimited):
		return "github_rate_limited", true
	case errors.Is(err, github.ErrUnavailable):
		return "github_unavailable", true
	}
	return "", false
}

// Start updates edelweissd to the release a fresh check offers, when confirmed is its SHA-256 (the one the user saw):
// it takes the shared lock and then the update lock, downloads and checks the new binary (Prepare), writes the plan
// and launches the helper, which gets the update lock. Until the launch every failure, a panic too, gives up both
// locks and removes what Start made. After it, the shared lock stays held: the helper restarts edelweissd, and only
// when it ends while this process still runs is the lock released.
//
// The shared lock comes first: its probe (the daemon's sharedLock) must not find the update lock Start itself takes.
func (s *Service) Start(ctx context.Context, confirmed string) (Offer, error) {
	f, r := s.examine()
	if r != ReasonNone {
		return Offer{}, &NotPossibleError{Reason: r}
	}
	off, err := Check(ctx, s.o.Releases, s.o.Source, s.o.Arch, s.o.Version)
	if err != nil {
		return Offer{}, err
	}
	s.keep(off)
	if !off.Available {
		return Offer{}, ErrNoUpdate
	}

	if !s.o.Lock.TryLock() {
		return Offer{}, ErrBusy
	}
	var lock *os.File
	var staged Staged
	launched, planned := false, false
	defer func() {
		if launched {
			return
		}
		if staged.Path != "" {
			_ = os.Remove(staged.Path)
		}
		if planned {
			_ = os.Remove(filepath.Join(s.dir, planFile))
			_ = os.Remove(filepath.Join(s.dir, "init.new"))
		}
		if lock != nil {
			_ = lock.Close() // releases the update lock
		}
		s.o.Lock.Unlock()
	}()
	if lock, err = acquireLock(s.dir); err != nil {
		if errors.Is(err, ErrHelperRunning) {
			return Offer{}, fmt.Errorf("%w: %w", ErrBusy, err)
		}
		return Offer{}, err
	}

	staged, err = Prepare(ctx, s.o.Releases, off, confirmed, s.o.Binary, s.o.MaxDownload, s.o.Run)
	if err != nil {
		return Offer{}, err
	}
	p := Plan{
		Binary:       s.o.Binary,
		Staged:       staged.Path,
		BinaryBackup: s.o.Binary + backupSuffix,
		InitScript:   filepath.Join(s.o.Root, "etc/init.d/edelweiss"),
		InitBackup:   filepath.Join(s.dir, "init"+backupSuffix),
		NewScript:    filepath.Join(s.dir, "init.new"),
		From:         s.o.Version,
		To:           off.Latest,
		HealthURL:    f.healthURL,
		Insecure:     f.insecure,
		Lbu:          f.mode != Sys,
		RcService:    f.tools.RcService,
		LbuTool:      f.tools.Lbu,
		Wait:         helperWait,
		Stable:       helperStable,
		Dir:          s.dir,
		Script:       staged.Script,
	}
	planned = true
	planPath, err := WritePlan(p)
	if err != nil {
		return Offer{}, err
	}
	done, err := s.o.Launcher.Launch(planPath, lock)
	if err != nil {
		return Offer{}, fmt.Errorf("selfupdate: starting the helper: %w", err)
	}
	if done == nil { // nothing would ever release the shared lock
		return Offer{}, errors.New("selfupdate: the launcher gives no way to learn the end of the helper")
	}
	launched = true
	_ = lock.Close() // the helper holds the update lock now
	go s.awaitHelper(done)
	return off, nil
}

// awaitHelper releases the shared lock when the helper ends while this process still runs; normally the helper
// restarts edelweissd first.
func (s *Service) awaitHelper(done <-chan error) {
	err := <-done
	if err != nil {
		s.o.Logger.Warn("self-update: the helper ended while edelweissd still runs", "err", err)
	} else {
		s.o.Logger.Info("self-update: the helper ended while edelweissd still runs")
	}
	s.o.Lock.Unlock()
}

// Dismiss removes the result of the last update.
func (s *Service) Dismiss() error { return Dismiss(s.dir) }
