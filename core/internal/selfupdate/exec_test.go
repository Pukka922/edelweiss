package selfupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func writeScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "script")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExecRunEnvironmentAndDir(t *testing.T) {
	out, err := ExecRun(context.Background(), writeScript(t, "env\npwd\n"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if last := lines[len(lines)-1]; last != "/" {
		t.Errorf("dir = %q, want /", last)
	}
	var env []string
	for _, l := range lines[:len(lines)-1] {
		// the shell adds these itself
		if strings.HasPrefix(l, "PWD=") || strings.HasPrefix(l, "OLDPWD=") || strings.HasPrefix(l, "SHLVL=") || strings.HasPrefix(l, "_=") {
			continue
		}
		env = append(env, l)
	}
	slices.Sort(env)
	if got := strings.Join(env, "|"); got != "LC_ALL=C|PATH=/usr/sbin:/usr/bin:/sbin:/bin" {
		t.Errorf("environment = %q", got)
	}
}

func TestExecRunErrorCarriesStderr(t *testing.T) {
	_, err := ExecRun(context.Background(), writeScript(t, "echo no good >&2\nexit 3\n"))
	if err == nil || !strings.Contains(err.Error(), "no good") {
		t.Errorf("err = %v", err)
	}
}

func TestExecRunTimeoutKillsTheGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	script := writeScript(t, "sleep 30 &\necho $! > "+pidFile+"\nsleep 30\n")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := ExecRun(ctx, script)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the deadline", err)
	}
	if d := time.Since(start); d > 2000*time.Millisecond { // the deadline, 500 ms, and at most 1.5 s more
		t.Errorf("returned after %v, want within 1.5 s of the deadline", d)
	}
	raw, rerr := os.ReadFile(pidFile)
	pid, perr := strconv.Atoi(strings.TrimSpace(string(raw)))
	if rerr != nil || perr != nil {
		t.Fatalf("the script did not record its child: %v, %v", rerr, perr)
	}
	// the background child is gone (or a zombie that its reaper has yet to collect) once ExecRun returned
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil || strings.Contains(string(stat), ") Z ") {
			break
		}
		if time.Now().After(deadline) {
			syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("the background child %d outlived ExecRun", pid)
		}
	}
}

// rc-service leaves its supervisor (supervise-daemon) running in its process group, which must survive the command.
func TestExecRunLeavesDescendantsOfASuccessfulCommand(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	script := writeScript(t, "sleep 30 >/dev/null 2>&1 </dev/null &\necho $! > "+pidFile+"\n")
	if _, err := ExecRun(context.Background(), script); err != nil {
		t.Fatal(err)
	}
	raw, rerr := os.ReadFile(pidFile)
	pid, perr := strconv.Atoi(strings.TrimSpace(string(raw)))
	if rerr != nil || perr != nil {
		t.Fatalf("the script did not record its child: %v, %v", rerr, perr)
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil || strings.Contains(string(stat), ") Z ") { // a killed child is a zombie until its reaper collects it
		t.Errorf("the background child %d did not survive ExecRun: %v %q", pid, err, stat)
	}
}

// A descendant that keeps the output pipe open does not make a command that succeeded fail.
func TestExecRunSucceedsWhileADescendantHoldsThePipe(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	script := writeScript(t, "sleep 30 &\necho $! > "+pidFile+"\necho done\n")
	start := time.Now()
	out, err := ExecRun(context.Background(), script)
	if raw, rerr := os.ReadFile(pidFile); rerr == nil {
		if pid, perr := strconv.Atoi(strings.TrimSpace(string(raw))); perr == nil {
			defer syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	if err != nil || strings.TrimSpace(string(out)) != "done" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("returned after %v", d)
	}
}
