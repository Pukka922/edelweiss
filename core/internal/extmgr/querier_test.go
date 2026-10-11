package extmgr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Pukka922/edelweiss/core/internal/testutil"
)

func newQuerier(t *testing.T) *ExecQuerier {
	t.Helper()
	q, err := NewExecQuerier()
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func TestQueryTestExt(t *testing.T) {
	bin, err := testutil.BuildTestExt()
	if err != nil {
		t.Fatal(err)
	}
	m, err := newQuerier(t).Query(context.Background(), bin)
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "testext" || m.IconSVG == "" || m.SDKVersion == "" {
		t.Fatalf("unexpected manifest %+v", m)
	}
}

func TestQueryNotExecutable(t *testing.T) {
	p := filepath.Join(testutil.TempDir(t), "garbage")
	if err := os.WriteFile(p, []byte("this is not a program"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := newQuerier(t).Query(context.Background(), p); !errors.Is(err, ErrWrongArch) {
		t.Fatalf("err = %v, want ErrWrongArch", err)
	}
}

func TestQueryTimeout(t *testing.T) {
	p := filepath.Join(testutil.TempDir(t), "sleeper")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nsleep 10\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	q := newQuerier(t)
	q.Timeout = 200 * time.Millisecond
	start := time.Now()
	_, err := q.Query(context.Background(), p)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want timeout error", err)
	}
	// the whole process group (sh and its sleep child) must die on timeout, so
	// Run does not have to wait for WaitDelay on the orphan's open pipe
	if d := time.Since(start); d >= time.Second {
		t.Fatalf("timeout took %s, want well under 1s", d)
	}
}

func TestQueryParentCancelled(t *testing.T) {
	p := filepath.Join(testutil.TempDir(t), "sleeper")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nsleep 10\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	q := newQuerier(t)
	q.Timeout = 10 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	start := time.Now()
	_, err := q.Query(ctx, p)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if strings.Contains(err.Error(), "timed out") {
		t.Fatalf("cancellation reported as timeout: %v", err)
	}
	if d := time.Since(start); d >= time.Second {
		t.Fatalf("cancel took %s, want well under 1s", d)
	}
}

// processGone reports whether pid has exited (a zombie that nobody has reaped
// yet counts as gone).
func processGone(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return true
	}
	i := strings.LastIndexByte(string(data), ')')
	return i < 0 || strings.HasPrefix(strings.TrimSpace(string(data[i+1:])), "Z")
}

// A query that succeeds may still leave processes behind (here a child that
// let go of the output pipes): the query's process group is killed whenever
// the query ends, not only on timeout.
func TestQueryKillsLeftoverProcesses(t *testing.T) {
	p := filepath.Join(testutil.TempDir(t), "forks")
	// the child's pid travels in the manifest's description
	script := "#!/bin/sh\nsleep 30 </dev/null >/dev/null 2>&1 &\n" +
		`echo "{\"id\":\"forks\",\"version\":\"1.0.0\",\"apiVersion\":1,\"name\":{\"en\":\"F\"},` +
		`\"description\":{\"en\":\"$!\"},\"menu\":{\"section\":\"tools\"}}"` + "\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	m, err := newQuerier(t).Query(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(m.Description["en"])
	if err != nil {
		t.Fatalf("child pid %q", m.Description["en"])
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	deadline := time.Now().Add(2 * time.Second)
	for !processGone(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("process %d left behind by the manifest query is still running", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A binary that exists but cannot be executed because its interpreter (for a
// dynamically linked program: the dynamic loader, missing on musl for a glibc
// build) does not exist makes execve fail with ENOENT – a confusing "no such
// file or directory" for a file that is right there.
func TestQueryMissingInterpreter(t *testing.T) {
	p := filepath.Join(testutil.TempDir(t), "dynamic")
	if err := os.WriteFile(p, []byte("#!/nonexistent/ld-linux-x86-64.so.2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := newQuerier(t).Query(context.Background(), p)
	if !errors.Is(err, ErrNotStatic) || !strings.Contains(err.Error(), "CGO_ENABLED=0") {
		t.Fatalf("err = %v, want ErrNotStatic", err)
	}
	// a binary that is really missing is not reported as one with a missing interpreter
	if _, err := newQuerier(t).Query(context.Background(), p+".missing"); err == nil || errors.Is(err, ErrNotStatic) {
		t.Fatalf("missing binary: err = %v", err)
	}
}

func TestQueryOutputTooLarge(t *testing.T) {
	p := filepath.Join(testutil.TempDir(t), "chatty")
	// 1100000 bytes > maxManifestSize (1 MiB = 1048576)
	script := "#!/bin/sh\nhead -c 1100000 /dev/zero | tr '\\0' a\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := newQuerier(t).Query(context.Background(), p)
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err = %v, want output too large error", err)
	}
}

func TestQueryInvalidManifest(t *testing.T) {
	p := filepath.Join(testutil.TempDir(t), "bad")
	if err := os.WriteFile(p, []byte("#!/bin/sh\necho '{\"id\":\"X\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := newQuerier(t).Query(context.Background(), p); err == nil {
		t.Fatal("expected manifest validation error")
	}
}

func TestQueryUnsupportedAPIVersion(t *testing.T) {
	p := filepath.Join(testutil.TempDir(t), "future")
	script := "#!/bin/sh\necho '{\"id\":\"future\",\"version\":\"1.0.0\",\"apiVersion\":2,\"name\":{\"en\":\"F\"},\"menu\":{\"section\":\"tools\"}}'\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := newQuerier(t).Query(context.Background(), p); !errors.Is(err, ErrUnsupportedAPI) {
		t.Fatalf("err = %v", err)
	}
}
