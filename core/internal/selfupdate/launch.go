package selfupdate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// Launcher starts the helper that swaps the binary. done gets the end of the helper, if edelweissd still runs then.
type Launcher interface {
	// Launch starts the helper with the update lock (AcquireLock) as its fd 3. The caller closes its own copy of the
	// lock after a successful launch; the child keeps it.
	Launch(planPath string, lock *os.File) (done <-chan error, err error)
}

// ExecLauncher starts Binary self-update-apply <plan> in a session of its own (Setsid; no Pdeathsig), so that
// stopping edelweiss – which is what the helper does – does not stop the helper. It has the null device for stdin,
// stdout and stderr and nothing of this process above fd 2. Binary is /proc/self/exe when empty: the inode edelweissd
// runs from, even after the swap renamed a new binary into its place.
type ExecLauncher struct{ Binary string }

func (l ExecLauncher) Launch(planPath string, lock *os.File) (<-chan error, error) {
	if lock == nil {
		return nil, errors.New("selfupdate: no update lock to hand on")
	}
	bin := l.Binary
	if bin == "" {
		bin = "/proc/self/exe"
	}
	cloexecFrom3()
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer null.Close()
	cmd := exec.Command(bin, "self-update-apply", planPath)
	cmd.Dir = "/"
	cmd.Stdin, cmd.Stdout, cmd.Stderr = null, null, null
	cmd.ExtraFiles = []*os.File{lock} // fd 3; Go dup2s it, so the close-on-exec flag of ours does not matter
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }() // also reaps the helper
	return done, nil
}

// cloexecFrom3 marks every fd from 3 on close-on-exec, so that the helper inherits nothing but the lock. In edelweissd
// this is defence in depth (nothing hands the server a listener); it is the pattern of the OpenRC extension, where
// the supervisor does.
func cloexecFrom3() {
	if err := unix.CloseRange(3, ^uint(0), unix.CLOSE_RANGE_CLOEXEC); err == nil {
		return
	}
	entries, _ := os.ReadDir("/proc/self/fd")
	for _, e := range entries {
		if fd, err := strconv.Atoi(e.Name()); err == nil && fd >= 3 {
			_, _ = unix.FcntlInt(uintptr(fd), unix.F_SETFD, unix.FD_CLOEXEC)
		}
	}
}

// The helper leaves the cgroup of the service edelweiss for one of its own: when OpenRC stops edelweiss, its cleanup
// kills the processes of that cgroup, and the helper is the one that restarts the service.
const (
	procCgroup    = "/proc/self/cgroup"
	escapeDir     = "/sys/fs/cgroup/edelweiss-selfupdate"
	serviceCgroup = "openrc.edelweiss"
)

// errStillInCgroup: the write to the new cgroup went through, and the process is still in that of the service (under
// OpenRC's hybrid cgroups the write silently does nothing).
var errStillInCgroup = errors.New("the process is still in the cgroup of the service")

// escapeCgroup moves the process pid out of its cgroup (listed in the file cgroups) into the cgroup dir when that is
// the cgroup of the OpenRC service edelweiss. Best effort: on failure the process stays where it is, and the caller
// gets the error; so it does when the file cgroups still names the service after the move (errStillInCgroup). A
// process in another cgroup stays there without an error, and so does one without a cgroup file (a kernel without
// cgroups).
func escapeCgroup(cgroups, dir string, pid int) error {
	data, err := os.ReadFile(cgroups)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !strings.Contains(string(data), serviceCgroup) {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(fmt.Sprint(pid)), 0o644); err != nil {
		return err
	}
	if after, err := os.ReadFile(cgroups); err == nil && strings.Contains(string(after), serviceCgroup) {
		return errStillInCgroup
	}
	return nil
}
