// Package procman runs one extension process per (user, admin mode, extension)
// with dropped privileges and hands requests to it over a private Unix socket.
package procman

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Pukka922/edelweiss/sdk"
)

var (
	ErrSpawn     = errors.New("extension process could not be started")
	ErrCrashLoop = errors.New("extension is crashing repeatedly")
)

const (
	crashWindow   = 60 * time.Second
	crashLimit    = 3
	crashCooldown = 30 * time.Second
	// stopTimeout must exceed the SDK's own 5 s graceful shutdown so that
	// SIGKILL is only a last resort.
	stopTimeout  = 8 * time.Second
	reapInterval = 30 * time.Second
	// waitDelay bounds how long Wait lingers for the output pipes after the
	// process exited: a descendant may keep them open indefinitely.
	waitDelay = 2 * time.Second
)

// Key identifies a process slot.
type Key struct {
	User  string
	Admin bool
	ExtID string
}

// Identity is the OS account a process runs as.
type Identity struct {
	Name     string
	UID, GID int
	GroupIDs []int
	Home     string
}

// Spec describes the process to run for a key.
type Spec struct {
	Key      Key
	Binary   string
	Identity Identity
	DevUIDir string
}

type Options struct {
	SocketDir   string
	IdleTimeout time.Duration
	Logger      *slog.Logger
	Now         func() time.Time
}

// Process is a running extension process.
type Process struct {
	key       Key
	socket    string
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    *logWriter
	stderr    *logWriter
	transport *http.Transport
	done      chan struct{}
	stopping  atomic.Bool
	active    int       // guarded by Manager.mu
	lastUsed  time.Time // guarded by Manager.mu
}

func (p *Process) Key() Key                     { return p.key }
func (p *Process) PID() int                     { return p.cmd.Process.Pid }
func (p *Process) Done() <-chan struct{}        { return p.done }
func (p *Process) Transport() http.RoundTripper { return p.transport }
func (p *Process) Dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", p.socket)
}

func (p *Process) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// Manager owns all extension processes.
type Manager struct {
	opts     Options
	mu       sync.Mutex
	procs    map[Key]*Process
	crashes  map[Key][]time.Time
	cooldown map[Key]time.Time
	seq      atomic.Uint64
}

func New(opts Options) (*Manager, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if err := os.MkdirAll(opts.SocketDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(opts.SocketDir, 0o700); err != nil {
		return nil, err
	}
	// The daemon owns the directory exclusively; sockets left behind by an
	// unclean shutdown would collide with new names (seq restarts at 1).
	entries, err := os.ReadDir(opts.SocketDir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sock") {
			continue
		}
		if err := os.Remove(filepath.Join(opts.SocketDir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	return &Manager{opts: opts, procs: map[Key]*Process{}, crashes: map[Key][]time.Time{}, cooldown: map[Key]time.Time{}}, nil
}

// Acquire returns the running process for spec.Key, starting it if needed.
// The returned release func must be called when the request (or stream) ends.
func (m *Manager) Acquire(ctx context.Context, spec Spec) (*Process, func(), error) {
	for _, name := range []string{spec.Key.User, spec.Key.ExtID} {
		if err := validateName(name); err != nil {
			return nil, nil, err
		}
	}
	m.mu.Lock()
	now := m.opts.Now()
	if until, ok := m.cooldown[spec.Key]; ok {
		if now.Before(until) {
			m.mu.Unlock()
			return nil, nil, ErrCrashLoop
		}
		delete(m.cooldown, spec.Key)
	}
	p := m.procs[spec.Key]
	if p == nil || p.exited() {
		var err error
		if p, err = m.spawn(spec); err != nil {
			m.mu.Unlock()
			return nil, nil, err
		}
		m.procs[spec.Key] = p
	}
	p.active++
	p.lastUsed = now
	m.mu.Unlock()

	var once sync.Once
	release := func() {
		once.Do(func() {
			m.mu.Lock()
			p.active--
			p.lastUsed = m.opts.Now()
			m.mu.Unlock()
		})
	}
	return p, release, nil
}

// spawn starts a process; m.mu must be held.
func (m *Manager) spawn(spec Spec) (*Process, error) {
	cred, err := credentialFor(spec.Identity)
	if err != nil {
		return nil, err
	}
	mode := "u"
	if spec.Key.Admin {
		mode = "a"
	}
	sock := filepath.Join(m.opts.SocketDir, fmt.Sprintf("%s.%s.%s.%d.sock", spec.Key.ExtID, spec.Key.User, mode, m.seq.Add(1)))
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("%w: listen: %v", ErrSpawn, err)
	}
	ln.SetUnlinkOnClose(false)
	lf, err := ln.File()
	_ = ln.Close()
	if err != nil {
		_ = os.Remove(sock)
		return nil, fmt.Errorf("%w: %v", ErrSpawn, err)
	}
	defer lf.Close()
	if err := os.Chmod(sock, 0o600); err != nil {
		_ = os.Remove(sock)
		return nil, fmt.Errorf("%w: %v", ErrSpawn, err)
	}

	logger := m.opts.Logger.With("ext", spec.Key.ExtID, "user", spec.Key.User, "admin", spec.Key.Admin)
	cmd := exec.Command(spec.Binary)
	cmd.Env = buildEnv(spec)
	cmd.Dir = "/"
	cmd.ExtraFiles = []*os.File{lf} // becomes FD 3
	stdout := &logWriter{logger: logger, level: slog.LevelInfo}
	stderr := &logWriter{logger: logger, level: slog.LevelWarn}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = waitDelay
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: cred, Setpgid: true, Pdeathsig: syscall.SIGTERM}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = os.Remove(sock)
		return nil, fmt.Errorf("%w: %v", ErrSpawn, err)
	}
	if err := startOnLockedThread(cmd); err != nil {
		_ = os.Remove(sock)
		return nil, fmt.Errorf("%w: %v", ErrSpawn, err)
	}

	dialer := &net.Dialer{Timeout: 5 * time.Second}
	p := &Process{
		key: spec.Key, socket: sock, cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr, done: make(chan struct{}),
		transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "unix", sock)
			},
			DisableCompression:  true,
			MaxIdleConnsPerHost: 16,
			IdleConnTimeout:     90 * time.Second,
		},
	}
	logger.Info("extension process started", "pid", cmd.Process.Pid)
	go m.wait(p, logger)
	return p, nil
}

func (m *Manager) wait(p *Process, logger *slog.Logger) {
	err := p.cmd.Wait()
	p.stdout.Flush()
	p.stderr.Flush()
	stopped := p.stopping.Load()
	// The leader is gone and has had its graceful window (stop() sends SIGTERM
	// first), so do not leave any descendant in its process group running as
	// the user - neither after a crash nor after a regular stop, where a member
	// that ignores SIGTERM and holds the output pipes would otherwise survive
	// (Wait returns waitDelay after the leader exited, before stopTimeout).
	if kerr := syscall.Kill(-p.PID(), syscall.SIGKILL); kerr != nil && !errors.Is(kerr, syscall.ESRCH) {
		logger.Warn("could not kill process group of extension", "pid", p.PID(), "err", kerr)
	}
	m.mu.Lock()
	if m.procs[p.key] == p {
		delete(m.procs, p.key)
	}
	if stopped {
		logger.Info("extension process stopped", "pid", p.PID())
	} else {
		logger.Warn("extension process exited unexpectedly", "pid", p.PID(), "err", err)
		now := m.opts.Now()
		recent := slices.DeleteFunc(append(m.crashes[p.key], now), func(t time.Time) bool { return now.Sub(t) > crashWindow })
		if len(recent) >= crashLimit {
			m.cooldown[p.key] = now.Add(crashCooldown)
			delete(m.crashes, p.key)
			logger.Error("extension crash loop detected, pausing restarts", "cooldown", crashCooldown)
		} else {
			m.crashes[p.key] = recent
		}
	}
	m.mu.Unlock()
	p.transport.CloseIdleConnections()
	_ = os.Remove(p.socket)
	close(p.done)
}

func (m *Manager) stop(p *Process) {
	p.stopping.Store(true)
	_ = p.stdin.Close()
	pid := p.PID()
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(stopTimeout):
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		<-p.done
	}
}

func (m *Manager) stopWhere(match func(Key) bool) {
	m.mu.Lock()
	var victims []*Process
	for k, p := range m.procs {
		if match(k) {
			victims = append(victims, p)
			delete(m.procs, k)
		}
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, p := range victims {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.stop(p)
		}()
	}
	wg.Wait()
}

// StopExtension stops all processes of an extension (e.g. after update/removal).
func (m *Manager) StopExtension(id string) { m.stopWhere(func(k Key) bool { return k.ExtID == id }) }

// StopUser stops the processes of a user (only admin-mode ones if adminOnly).
func (m *Manager) StopUser(user string, adminOnly bool) {
	m.stopWhere(func(k Key) bool { return k.User == user && (!adminOnly || k.Admin) })
}

// StopAll stops every process.
func (m *Manager) StopAll() { m.stopWhere(func(Key) bool { return true }) }

// Running returns the keys of all live processes.
func (m *Manager) Running() []Key {
	m.mu.Lock()
	defer m.mu.Unlock()
	var keys []Key
	for k, p := range m.procs {
		if !p.exited() {
			keys = append(keys, k)
		}
	}
	return keys
}

// Reap stops processes that were idle for IdleTimeout and do not report busy.
func (m *Manager) Reap(ctx context.Context) {
	m.mu.Lock()
	now := m.opts.Now()
	var idle []*Process
	for _, p := range m.procs {
		if p.active == 0 && now.Sub(p.lastUsed) >= m.opts.IdleTimeout {
			idle = append(idle, p)
		}
	}
	m.mu.Unlock()
	for _, p := range idle {
		if m.busy(ctx, p) {
			m.mu.Lock()
			p.lastUsed = m.opts.Now()
			m.mu.Unlock()
			continue
		}
		m.mu.Lock()
		if m.procs[p.key] != p || p.active > 0 || m.opts.Now().Sub(p.lastUsed) < m.opts.IdleTimeout {
			m.mu.Unlock()
			continue
		}
		delete(m.procs, p.key)
		m.mu.Unlock()
		m.stop(p)
	}
}

func (m *Manager) busy(ctx context.Context, p *Process) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://edelweiss-ext"+sdk.StatusPath, nil)
	if err != nil {
		return false
	}
	resp, err := p.transport.RoundTrip(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var s struct {
		Busy bool `json:"busy"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&s) != nil {
		return false
	}
	return s.Busy
}

// Run reaps idle processes periodically and stops everything when ctx ends.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(reapInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.StopAll()
			return
		case <-t.C:
			m.Reap(ctx)
		}
	}
}
