package procman

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Pukka922/edelweiss/core/internal/testutil"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newTestManager(t *testing.T) (*Manager, *fakeClock) {
	t.Helper()
	clk := &fakeClock{t: time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)}
	m, err := New(Options{SocketDir: filepath.Join(t.TempDir(), "sockets"), IdleTimeout: 10 * time.Minute, Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.StopAll)
	return m, clk
}

func currentIdentity(t *testing.T) Identity {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	return Identity{Name: u.Username, UID: uid, GID: gid, Home: u.HomeDir}
}

func testSpec(t *testing.T) Spec {
	t.Helper()
	bin, err := testutil.BuildTestExt()
	if err != nil {
		t.Fatal(err)
	}
	id := currentIdentity(t)
	return Spec{Key: Key{User: id.Name, ExtID: "testext"}, Binary: bin, Identity: id}
}

func get(t *testing.T, p *Process, path string) *http.Response {
	t.Helper()
	client := &http.Client{Transport: p.Transport(), Timeout: 5 * time.Second}
	resp, err := client.Get("http://edelweiss-ext" + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

func waitDone(t *testing.T, p *Process) {
	t.Helper()
	select {
	case <-p.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("process did not exit")
	}
}

func TestAcquireSpawnsAndReuses(t *testing.T) {
	m, _ := newTestManager(t)
	spec := testSpec(t)
	ctx := context.Background()

	p, release, err := m.Acquire(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	resp := get(t, p, "/hello")
	var hello struct {
		OSUid int `json:"osUid"`
	}
	err = json.NewDecoder(resp.Body).Decode(&hello)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("decode /hello: %v", err)
	}
	if hello.OSUid != os.Getuid() {
		t.Fatalf("osUid = %d", hello.OSUid)
	}
	release()
	release() // idempotent

	p2, release2, err := m.Acquire(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	defer release2()
	if p2.PID() != p.PID() {
		t.Fatal("process not reused")
	}

	adminSpec := spec
	adminSpec.Key.Admin = true
	p3, release3, err := m.Acquire(ctx, adminSpec)
	if err != nil {
		t.Fatal(err)
	}
	defer release3()
	if p3.PID() == p.PID() {
		t.Fatal("different key shared a process")
	}
	if fi, _ := os.Stat(m.opts.SocketDir); fi.Mode().Perm() != 0o700 {
		t.Fatalf("socket dir mode %v", fi.Mode().Perm())
	}
}

func TestReapIdleProcess(t *testing.T) {
	m, clk := newTestManager(t)
	p, release, err := m.Acquire(context.Background(), testSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	get(t, p, "/hello").Body.Close()
	release()

	clk.Advance(5 * time.Minute)
	m.Reap(context.Background())
	if len(m.Running()) != 1 {
		t.Fatal("reaped before idle timeout")
	}
	clk.Advance(6 * time.Minute)
	m.Reap(context.Background())
	waitDone(t, p)
	if len(m.Running()) != 0 {
		t.Fatal("still running after reap")
	}
}

func TestActiveProcessIsNotReaped(t *testing.T) {
	m, clk := newTestManager(t)
	p, release, err := m.Acquire(context.Background(), testSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	clk.Advance(time.Hour)
	m.Reap(context.Background())
	select {
	case <-p.Done():
		t.Fatal("active process was reaped")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestBusyProcessIsNotReaped(t *testing.T) {
	m, clk := newTestManager(t)
	spec := testSpec(t)
	ctx := context.Background()
	p, release, err := m.Acquire(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	get(t, p, "/busy?on=1").Body.Close()
	release()
	clk.Advance(11 * time.Minute)
	m.Reap(ctx)
	select {
	case <-p.Done():
		t.Fatal("busy process was reaped")
	case <-time.After(300 * time.Millisecond):
	}

	p, release, err = m.Acquire(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	get(t, p, "/busy?on=0").Body.Close()
	release()
	clk.Advance(11 * time.Minute)
	m.Reap(ctx)
	waitDone(t, p)
}

func TestCrashLoopBackoff(t *testing.T) {
	m, clk := newTestManager(t)
	script := filepath.Join(testutil.TempDir(t), "crash")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	spec := testSpec(t)
	spec.Binary = script
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		p, release, err := m.Acquire(ctx, spec)
		if err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		release()
		waitDone(t, p)
	}
	if _, _, err := m.Acquire(ctx, spec); !errors.Is(err, ErrCrashLoop) {
		t.Fatalf("err = %v, want ErrCrashLoop", err)
	}
	clk.Advance(31 * time.Second)
	p, release, err := m.Acquire(ctx, spec)
	if err != nil {
		t.Fatalf("after cooldown: %v", err)
	}
	release()
	waitDone(t, p)
}

func TestStopUserAndExtension(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	spec := testSpec(t)
	adminSpec := spec
	adminSpec.Key.Admin = true

	pu, ru, err := m.Acquire(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	ru()
	pa, ra, err := m.Acquire(ctx, adminSpec)
	if err != nil {
		t.Fatal(err)
	}
	ra()

	m.StopUser(spec.Key.User, true)
	waitDone(t, pa)
	select {
	case <-pu.Done():
		t.Fatal("non-admin process stopped by StopUser(adminOnly)")
	default:
	}
	m.StopExtension("testext")
	waitDone(t, pu)
}

func TestForeignUserRequiresRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	m, _ := newTestManager(t)
	spec := testSpec(t)
	spec.Identity = Identity{Name: "someone-else", UID: os.Getuid() + 1, GID: os.Getgid()}
	if _, _, err := m.Acquire(context.Background(), spec); !errors.Is(err, ErrSpawn) {
		t.Fatalf("err = %v, want ErrSpawn", err)
	}
}

// syncBuffer is a concurrency-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// writeScript writes an executable shell script that other users may run.
func writeScript(t *testing.T, body string) string {
	t.Helper()
	script := filepath.Join(testutil.TempDir(t), "ext.sh")
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

// processAlive reports whether pid exists and is not a zombie.
func processAlive(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 >= len(s) {
		return false
	}
	return s[i+2] != 'Z' && s[i+2] != 'X'
}

func TestCrashedExtWithLingeringChild(t *testing.T) {
	m, _ := newTestManager(t)
	pidFile := filepath.Join(testutil.TempDir(t), "child.pid")
	// The extension crashes, but a background child keeps its stdout/stderr
	// pipes open for 30 s.
	spec := testSpec(t)
	spec.Binary = writeScript(t, "#!/bin/sh\nsleep 30 &\necho $! > "+pidFile+"\nexit 3\n")

	p, release, err := m.Acquire(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case <-p.Done():
	case <-time.After(8 * time.Second):
		t.Fatal("Done() not closed after the extension crashed: a descendant holds the output pipes")
	}
	if len(m.Running()) != 0 {
		t.Fatalf("crashed extension still listed as running: %v", m.Running())
	}

	// The orphan must not keep running as the user.
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("pid file %q: %v", raw, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("orphaned child %d survived the crash of its extension", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A group member that ignores SIGTERM and holds the output pipes must not
// survive a regular stop either: once the leader is gone the whole process
// group is killed.
func TestStopKillsTermIgnoringGroupMember(t *testing.T) {
	m, _ := newTestManager(t)
	pidFile := filepath.Join(testutil.TempDir(t), "child.pid")
	// TERM is ignored while the child is forked, so there is no window in
	// which it could still die from SIGTERM; the leader then restores the
	// default and waits (SIGTERM kills it).
	spec := testSpec(t)
	spec.Binary = writeScript(t, "#!/bin/sh\ntrap '' TERM\nsleep 30 &\ntrap - TERM\necho $! > "+pidFile+"\nwait\n")

	_, release, err := m.Acquire(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	pid := 0
	for deadline := time.Now().Add(5 * time.Second); pid == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("extension did not start its background child")
		}
		if raw, err := os.ReadFile(pidFile); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
		}
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	status, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		t.Fatal(err)
	}
	if !sigtermIgnored(t, string(status)) {
		t.Fatalf("test setup: child %d does not ignore SIGTERM", pid)
	}

	m.StopExtension("testext")

	deadline := time.Now().Add(3 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("SIGTERM-ignoring group member %d survived StopExtension", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// sigtermIgnored parses the SigIgn mask of a /proc/<pid>/status dump.
func sigtermIgnored(t *testing.T, status string) bool {
	t.Helper()
	for _, line := range strings.Split(status, "\n") {
		if mask, ok := strings.CutPrefix(line, "SigIgn:"); ok {
			v, err := strconv.ParseUint(strings.TrimSpace(mask), 16, 64)
			if err != nil {
				t.Fatalf("SigIgn %q: %v", mask, err)
			}
			return v&(1<<(uint(syscall.SIGTERM)-1)) != 0
		}
	}
	t.Fatal("no SigIgn line in /proc status")
	return false
}

func TestReapSkipsProcessUsedDuringBusyCheck(t *testing.T) {
	m, clk := newTestManager(t)
	spec := testSpec(t)
	ctx := context.Background()
	p, release, err := m.Acquire(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	get(t, p, "/hello").Body.Close()
	release()
	clk.Advance(11 * time.Minute)

	// A request for the process arrives while Reap is asking it for its status.
	var checks atomic.Int32
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		checks.Add(1)
		_, rel, err := m.Acquire(ctx, spec)
		if err != nil {
			t.Errorf("Acquire during busy check: %v", err)
		} else {
			rel()
		}
		return dialer.DialContext(ctx, "unix", p.socket)
	}}
	m.mu.Lock()
	p.transport = tr
	m.mu.Unlock()

	m.Reap(ctx)
	if checks.Load() == 0 {
		t.Fatal("Reap did not run the busy check")
	}
	select {
	case <-p.Done():
		t.Fatal("process that was used during the busy check was reaped")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestNewRemovesStaleSockets(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sockets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "testext.alice.u.1.sock")
	keep := filepath.Join(dir, "notes.txt")
	for _, f := range []string{stale, keep} {
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := New(Options{SocketDir: dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale socket still present: %v", err)
	}
	if _, err := os.Lstat(keep); err != nil {
		t.Fatalf("unrelated file was removed: %v", err)
	}
}

func TestValidateName(t *testing.T) {
	for _, tc := range []struct {
		name string
		ok   bool
	}{
		{"alice", true},
		{"alice.smith", true},
		{"my-ext_2", true},
		{strings.Repeat("u", 32), true},
		{"", false},
		{".", false},
		{"..", false},
		{"a/b", false},
		{"../x", false},
		{"/", false},
		{strings.Repeat("u", 33), false},
		{"a\x00b", false},
		{"\x00", false},
	} {
		if err := validateName(tc.name); (err == nil) != tc.ok {
			t.Errorf("validateName(%q) = %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}

func TestAcquireRejectsUnsafeNames(t *testing.T) {
	m, _ := newTestManager(t)
	for _, tc := range []struct{ name, user, ext string }{
		{"slash in user", "a/b", "testext"},
		{"path traversal in user", "../../etc", "testext"},
		{"empty user", "", "testext"},
		{"dotdot user", "..", "testext"},
		{"long user", strings.Repeat("u", 33), "testext"},
		{"NUL in user", "al\x00ice", "testext"},
		{"NUL in extension", "alice", "te\x00xt"},
		{"slash in extension", "alice", "a/b"},
		{"empty extension", "alice", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := testSpec(t)
			spec.Key.User, spec.Key.ExtID = tc.user, tc.ext
			if _, _, err := m.Acquire(context.Background(), spec); !errors.Is(err, ErrSpawn) {
				t.Fatalf("err = %v, want ErrSpawn", err)
			}
		})
	}
	if n := len(m.Running()); n != 0 {
		t.Fatalf("%d processes started", n)
	}
	if entries, err := os.ReadDir(m.opts.SocketDir); err != nil || len(entries) != 0 {
		t.Fatalf("socket dir not empty: %v %v", entries, err)
	}
}

func TestLogWriterFlushLogsPartialLine(t *testing.T) {
	var buf syncBuffer
	w := &logWriter{logger: slog.New(slog.NewTextHandler(&buf, nil)), level: slog.LevelInfo}
	if _, err := w.Write([]byte("complete\npartial")); err != nil {
		t.Fatal(err)
	}
	if out := buf.String(); !strings.Contains(out, "complete") || strings.Contains(out, "partial") {
		t.Fatalf("before Flush: %q", out)
	}
	w.Flush()
	out := buf.String()
	if !strings.Contains(out, "partial") {
		t.Fatalf("Flush did not log the trailing partial line: %q", out)
	}
	w.Flush()
	if buf.String() != out {
		t.Fatalf("second Flush logged again: %q", buf.String())
	}
}

func TestOutputWithoutTrailingNewlineIsLogged(t *testing.T) {
	var buf syncBuffer
	m, err := New(Options{
		SocketDir: filepath.Join(t.TempDir(), "sockets"),
		Logger:    slog.New(slog.NewTextHandler(&buf, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.StopAll)
	spec := testSpec(t)
	spec.Binary = writeScript(t, "#!/bin/sh\nprintf out-tail\nprintf err-tail >&2\nexit 3\n")
	p, release, err := m.Acquire(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	release()
	waitDone(t, p)
	if out := buf.String(); !strings.Contains(out, "out-tail") || !strings.Contains(out, "err-tail") {
		t.Fatalf("trailing output lost: %q", out)
	}
}
