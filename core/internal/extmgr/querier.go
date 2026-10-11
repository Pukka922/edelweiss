// Package extmgr manages installed extensions in the extension store.
package extmgr

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"
	"time"

	"github.com/Pukka922/edelweiss/sdk"
	"github.com/Pukka922/edelweiss/sdk/manifest"
)

var (
	ErrWrongArch      = errors.New("binary cannot be executed on this system (wrong architecture or not a Linux executable)")
	ErrNotStatic      = errors.New("binary is dynamically linked or its interpreter is missing; build extensions with CGO_ENABLED=0")
	ErrUnsupportedAPI = errors.New("extension requires an unsupported apiVersion")
)

// Querier reads the manifest of an extension binary.
type Querier interface {
	Query(ctx context.Context, binary string) (*manifest.Manifest, error)
}

// ExecQuerier runs `<binary> --edelweiss-manifest`, as user nobody when
// edelweissd runs as root.
type ExecQuerier struct {
	Timeout    time.Duration
	Credential *syscall.Credential
}

func NewExecQuerier() (*ExecQuerier, error) {
	q := &ExecQuerier{Timeout: 5 * time.Second}
	if os.Geteuid() != 0 {
		return q, nil
	}
	u, err := user.Lookup("nobody")
	if err != nil {
		return nil, fmt.Errorf("lookup user nobody: %w", err)
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return nil, err
	}
	gid, err := strconv.ParseUint(u.Gid, 10, 32)
	if err != nil {
		return nil, err
	}
	q.Credential = &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: []uint32{}}
	return q, nil
}

const maxManifestSize = 1 << 20

func (q *ExecQuerier) Query(ctx context.Context, binary string) (*manifest.Manifest, error) {
	var out []byte
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		out, err = q.run(ctx, binary)
		if !errors.Is(err, syscall.ETXTBSY) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		return nil, err
	}
	m, err := manifest.Parse(out)
	if err != nil {
		return nil, err
	}
	if !manifest.IsSupportedAPIVersion(m.APIVersion) {
		return nil, fmt.Errorf("%w: %d", ErrUnsupportedAPI, m.APIVersion)
	}
	return m, nil
}

func (q *ExecQuerier) run(parent context.Context, binary string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, q.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, sdk.ManifestFlag)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8"}
	cmd.Dir = "/"
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: q.Credential, Setpgid: true}
	// The child leads its own process group (Setpgid); kill the whole group on
	// timeout or cancellation so descendants cannot keep the pipes open.
	killGroup := func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.Cancel = killGroup
	cmd.WaitDelay = time.Second
	stdout := &capBuffer{max: maxManifestSize}
	stderr := &capBuffer{max: 4096}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	if cmd.Process != nil {
		// Whatever the outcome, nothing the query started may outlive it: a
		// descendant that let go of the pipes would otherwise keep running.
		_ = killGroup()
	}
	switch {
	case errors.Is(err, syscall.ENOEXEC):
		return nil, ErrWrongArch
	case errors.Is(err, syscall.ENOENT) && cmd.Process == nil && exists(binary):
		// execve reports a missing interpreter as ENOENT although the binary is
		// there: typically a glibc build whose dynamic loader musl lacks.
		return nil, ErrNotStatic
	case errors.Is(err, syscall.ETXTBSY):
		return nil, err
	case parent.Err() != nil:
		return nil, fmt.Errorf("manifest query aborted: %w", parent.Err())
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return nil, fmt.Errorf("manifest query timed out after %s", q.Timeout)
	case err != nil:
		return nil, fmt.Errorf("manifest query failed: %v: %s", err, bytes.TrimSpace(stderr.buf.Bytes()))
	case stdout.overflow:
		return nil, errors.New("manifest output too large")
	}
	return stdout.buf.Bytes(), nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// capBuffer keeps at most max bytes and records overflow.
type capBuffer struct {
	buf      bytes.Buffer
	max      int
	overflow bool
}

func (b *capBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.buf.Len(); len(p) > room {
		b.overflow = true
		if room > 0 {
			b.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return b.buf.Write(p)
}
