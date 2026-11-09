package selfupdate

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Runner runs a program and returns what it printed on stdout. In production it is ExecRun.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

var (
	ErrReleaseChanged   = errors.New("the release changed since it was offered")
	ErrChecksumMismatch = errors.New("the download does not match its checksum")
	ErrWrongVersion     = errors.New("the downloaded binary is not the offered version")
	ErrNotRunnable      = errors.New("the downloaded binary does not run on this machine")
	ErrNoSpace          = errors.New("not enough free space next to the binary")
	ErrBadScript        = errors.New("the downloaded binary gives no usable init script")
)

// Staged is a new edelweissd that was downloaded, checked and placed next to the running one.
type Staged struct {
	Path    string // <binary>.edelweiss-new
	Version string
	Script  string // the init script the new binary makes for the service
}

const (
	// spaceReserve is the free space needed beyond the size of the download.
	spaceReserve = 16 << 20
	// runTimeout bounds each run of the new binary.
	runTimeout = 5 * time.Second
	// stagedSuffix is what the staged binary's name adds to the binary's.
	stagedSuffix = ".edelweiss-new"
	// backupSuffix is what the backup's name adds to the binary's.
	backupSuffix = ".edelweiss-bak"
	// maxRunOutput bounds what ExecRun keeps of stdout.
	maxRunOutput = 1 << 20
)

// sweepLeftovers removes from the directory of binary what a crash of an earlier update may have left there: download
// temp files (.edelweissd-dl-<16 hex>) and the temp files of the helper's copies (.<binary>-<digits> and
// .<binary>.edelweiss-bak-<digits>, see stageFile). Only regular files with exactly these names go; errors are ignored.
func sweepLeftovers(binary string) {
	dir, base := filepath.Dir(binary), regexp.QuoteMeta(filepath.Base(binary))
	leftover := regexp.MustCompile(`^(\.edelweissd-dl-[0-9a-f]{16}|\.` + base + `(` + regexp.QuoteMeta(backupSuffix) + `)?-[0-9]+)$`)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Type().IsRegular() && leftover.MatchString(e.Name()) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// freeSpace tells how many bytes a non-root user can still write in dir; a variable for the tests.
var freeSpace = func(dir string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	size := uint64(st.Frsize) // the unit of Bavail
	if size == 0 {
		size = uint64(st.Bsize)
	}
	return st.Bavail * size, nil
}

// Prepare downloads the offered binary next to binary, checks it against the confirmed digest and runs it to learn
// its version and the init script it makes for binary. Only when all that is right is it renamed to
// <binary>.edelweiss-new. On any error or panic nothing that Prepare created remains, and nothing else was changed.
// First it removes what a crash of an earlier update left next to binary (sweepLeftovers), so the caller holds the
// update lock.
// The free space must hold three times the download and spaceReserve: the staged binary, the helper's backup of the
// running one and the temp copy of a rollback, while the old inode is still in use.
func Prepare(ctx context.Context, r Releases, o Offer, confirmed string, binary string, maxDownload int64, run Runner) (st Staged, err error) {
	if o.SHA256 == "" || confirmed != o.SHA256 {
		return Staged{}, ErrReleaseChanged
	}
	if !filepath.IsAbs(binary) {
		return Staged{}, fmt.Errorf("selfupdate: the binary path %q is not absolute", binary)
	}
	dir := filepath.Dir(binary)
	sweepLeftovers(binary)
	free, err := freeSpace(dir)
	if err != nil {
		return Staged{}, fmt.Errorf("checking the free space in %s: %w", dir, err)
	}
	if need := 3*uint64(o.Size) + spaceReserve; free < need {
		return Staged{}, fmt.Errorf("%w: %s has %d bytes free, %d are needed", ErrNoSpace, dir, free, need)
	}
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return Staged{}, err
	}
	tmp := filepath.Join(dir, ".edelweissd-dl-"+hex.EncodeToString(rnd[:]))
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Staged{}, err
	}
	renamed := false
	defer func() { // on an error and on a panic (of the Runner, say)
		if !renamed {
			_ = f.Close() // a second Close only fails
			_ = os.Remove(tmp)
		}
	}()
	h := sha256.New()
	_, err = r.Download(ctx, o.asset, io.MultiWriter(f, h), maxDownload)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Staged{}, err
	}
	if sum := hex.EncodeToString(h.Sum(nil)); sum != o.SHA256 {
		return Staged{}, fmt.Errorf("%w: %s has SHA-256 %s, the release lists %s", ErrChecksumMismatch, o.Asset, sum, o.SHA256)
	}
	if err = os.Chmod(tmp, 0o755); err != nil {
		return Staged{}, err
	}

	vctx, cancel := context.WithTimeout(ctx, runTimeout)
	out, err := run(vctx, tmp, "version")
	cancel()
	if ctx.Err() != nil { // the caller gave up, which says nothing about the binary
		return Staged{}, fmt.Errorf("running the new binary: %w", ctx.Err())
	}
	if err != nil {
		return Staged{}, fmt.Errorf("%w: %v", ErrNotRunnable, err)
	}
	if v := strings.TrimSpace(string(out)); v != o.Latest {
		return Staged{}, fmt.Errorf("%w: it says %q, the release is %q", ErrWrongVersion, v, o.Latest)
	}

	sctx, cancel := context.WithTimeout(ctx, runTimeout)
	out, err = run(sctx, tmp, "service", "script", "--binary", binary)
	cancel()
	if ctx.Err() != nil {
		return Staged{}, fmt.Errorf("running the new binary: %w", ctx.Err())
	}
	if err != nil {
		return Staged{}, fmt.Errorf("%w: %v", ErrBadScript, err)
	}
	script := string(out)
	if !strings.HasPrefix(script, "#!/sbin/openrc-run") || !strings.Contains(script, `command="`+binary+`"`) {
		return Staged{}, fmt.Errorf("%w: it is not an openrc-run script for %s", ErrBadScript, binary)
	}

	staged := binary + stagedSuffix
	if err = os.Rename(tmp, staged); err != nil {
		return Staged{}, err
	}
	renamed = true
	return Staged{Path: staged, Version: o.Latest, Script: script}, nil
}

// ExecRun is the production Runner: name runs with the environment PATH=/usr/sbin:/usr/bin:/sbin:/bin and LC_ALL=C,
// in /, without a shell, in a process group of its own that is killed with the context (not when the command returns).
// It returns stdout; on an error the first 4096 bytes of stderr are in the message. A start that fails with ETXTBSY
// (the file was just written) is tried again.
func ExecRun(ctx context.Context, name string, args ...string) ([]byte, error) {
	var out []byte
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		out, err = execRunOnce(ctx, name, args)
		if !errors.Is(err, syscall.ETXTBSY) {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return out, err
}

func execRunOnce(ctx context.Context, name string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	cmd.Dir = "/"
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// The child leads its own process group; kill the whole group on timeout or cancellation so that descendants
	// cannot keep the pipes open.
	killGroup := func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.Cancel = killGroup
	cmd.WaitDelay = time.Second
	stdout := &capBuffer{max: maxRunOutput}
	stderr := &capBuffer{max: 4096}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	// No kill after the command returned: what it started stays, as the supervise-daemon of rc-service restart must. A
	// descendant that keeps the pipes open only costs WaitDelay and does not fail a command that exited with success.
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	switch {
	case errors.Is(err, syscall.ETXTBSY):
		return nil, err
	case ctx.Err() != nil:
		return nil, fmt.Errorf("%s: %w", name, ctx.Err())
	case err != nil:
		return nil, fmt.Errorf("%s: %v: %s", name, err, bytes.TrimSpace(stderr.buf.Bytes()))
	case stdout.overflow:
		return nil, fmt.Errorf("%s: output too large", name)
	}
	return stdout.buf.Bytes(), nil
}

// capBuffer keeps at most max bytes and records that more came.
type capBuffer struct {
	buf      bytes.Buffer
	max      int
	overflow bool
}

func (b *capBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if room := b.max - b.buf.Len(); n > room {
		b.overflow = true
		p = p[:max(room, 0)] // the rest is swallowed, so that the child is not stopped by a broken pipe
	}
	b.buf.Write(p)
	return n, nil
}
