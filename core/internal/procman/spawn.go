package procman

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/Pukka922/edelweiss/sdk"
)

var (
	spawnOnce sync.Once
	spawnReqs chan spawnReq
)

type spawnReq struct {
	cmd    *exec.Cmd
	result chan error
}

// startOnLockedThread starts cmd from one goroutine that is locked to its OS
// thread forever. Pdeathsig is bound to the creating thread, so this keeps it
// valid for the lifetime of edelweissd.
func startOnLockedThread(cmd *exec.Cmd) error {
	spawnOnce.Do(func() {
		spawnReqs = make(chan spawnReq)
		go func() {
			runtime.LockOSThread()
			for r := range spawnReqs {
				r.result <- r.cmd.Start()
			}
		}()
	})
	r := spawnReq{cmd: cmd, result: make(chan error, 1)}
	spawnReqs <- r
	return <-r.result
}

// credentialFor returns the credential to run as id. Without root privileges
// only the current user is possible (development and tests).
func credentialFor(id Identity) (*syscall.Credential, error) {
	euid := os.Geteuid()
	if euid == 0 {
		groups := make([]uint32, 0, len(id.GroupIDs))
		for _, g := range id.GroupIDs {
			groups = append(groups, uint32(g))
		}
		return &syscall.Credential{Uid: uint32(id.UID), Gid: uint32(id.GID), Groups: groups}, nil
	}
	if id.UID == euid {
		return nil, nil
	}
	return nil, fmt.Errorf("%w: edelweissd must run as root to start processes for user %q", ErrSpawn, id.Name)
}

func buildEnv(spec Spec) []string {
	home := spec.Identity.Home
	if home == "" {
		home = "/"
	}
	env := []string{
		"HOME=" + home,
		"USER=" + spec.Identity.Name,
		"LOGNAME=" + spec.Identity.Name,
		"SHELL=/bin/sh",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"LANG=C.UTF-8",
		sdk.EnvListenFD + "=3",
		sdk.EnvExtensionID + "=" + spec.Key.ExtID,
		sdk.EnvAPIVersion + "=" + strconv.Itoa(sdk.APIVersion),
	}
	if spec.DevUIDir != "" {
		env = append(env, sdk.EnvDevUIDir+"="+spec.DevUIDir)
	}
	return env
}

// logWriter forwards process output line by line to slog.
type logWriter struct {
	mu     sync.Mutex
	logger *slog.Logger
	level  slog.Level
	buf    []byte
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		if line := strings.TrimRight(string(w.buf[:i]), "\r"); line != "" {
			w.logger.Log(context.Background(), w.level, "extension output", "line", line)
		}
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) > 4096 {
		w.logger.Log(context.Background(), w.level, "extension output", "line", string(w.buf))
		w.buf = w.buf[:0]
	}
	return len(p), nil
}

// Flush logs a trailing line that was not terminated by a newline. It is
// called once the process output is complete.
func (w *logWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if line := strings.TrimRight(string(w.buf), "\r"); line != "" {
		w.logger.Log(context.Background(), w.level, "extension output", "line", line)
	}
	w.buf = nil
}

// maxNameLen bounds user names and extension IDs used in socket file names.
const maxNameLen = 32

// validateName rejects user names and extension IDs that are unsafe to embed
// in the socket path.
func validateName(s string) error {
	if s == "" || s == "." || s == ".." || len(s) > maxNameLen || strings.Contains(s, "/") || strings.ContainsRune(s, 0) {
		return fmt.Errorf("%w: invalid name %q", ErrSpawn, s)
	}
	return nil
}
