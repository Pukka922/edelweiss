package selfupdate

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The test binary plays the child of some tests when testModeVar is set.
const (
	testModeVar   = "EDELWEISS_SELFUPDATE_TEST_MODE"
	testReportVar = "EDELWEISS_SELFUPDATE_TEST_REPORT"
	testLockVar   = "EDELWEISS_SELFUPDATE_TEST_LOCK"
	testGoVar     = "EDELWEISS_SELFUPDATE_TEST_GO"
	testDirVar    = "EDELWEISS_SELFUPDATE_TEST_DIR"
)

func TestMain(m *testing.M) {
	switch os.Getenv(testModeVar) {
	case "apply":
		if len(os.Args) == 3 && os.Args[1] == "self-update-apply" {
			os.Exit(reportAsHelper(os.Args[2]))
		}
	case "holdlock":
		os.Exit(holdLock(os.Getenv(testLockVar)))
	case "runhelper":
		if len(os.Args) == 3 && os.Args[1] == "self-update-apply" {
			os.Exit(runHelperChild(os.Args[2]))
		}
	}
	os.Exit(m.Run())
}

// sessionOf reads the session id from /proc/<pid>/stat.
func sessionOf(pid string) (string, error) {
	data, err := os.ReadFile("/proc/" + pid + "/stat")
	if err != nil {
		return "", err
	}
	rest := string(data[strings.LastIndexByte(string(data), ')')+2:])
	f := strings.Fields(rest) // state ppid pgrp session
	return f[3], nil
}

// reportAsHelper writes what the launched process sees of itself to the report file, then waits for the go file.
func reportAsHelper(planPath string) int {
	sid, _ := sessionOf("self")
	wd, _ := os.Getwd()
	lines := []string{"plan=" + planPath, "session=" + sid, "cwd=" + wd}
	fds, _ := os.ReadDir("/proc/self/fd")
	for _, e := range fds {
		if target, err := os.Readlink("/proc/self/fd/" + e.Name()); err == nil {
			lines = append(lines, "fd"+e.Name()+"="+target)
		}
	}
	// the inherited lock
	lock, err := AdoptLock(os.Getenv(testDirVar))
	switch {
	case err == nil:
		lines = append(lines, "adopt=ok")
		defer lock.Close()
		// a command of the helper must not inherit the lock
		out, _ := exec.Command("sh", "-c", "ls -l /proc/self/fd").Output()
		lines = append(lines, "childSeesLock="+map[bool]string{true: "yes", false: "no"}[strings.Contains(string(out), "/lock")])
		// and a probe from a separate open finds it held
		if probe, err := os.Open(filepath.Join(os.Getenv(testDirVar), "lock")); err == nil {
			err := syscall.Flock(int(probe.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
			lines = append(lines, "held="+map[bool]string{true: "yes", false: "no"}[err == syscall.EWOULDBLOCK])
			probe.Close()
		}
	case errors.Is(err, ErrNoHelperLock):
		lines = append(lines, "adopt=refused", "adoptErr="+err.Error())
	default:
		lines = append(lines, "adopt=error:"+err.Error())
	}
	report := os.Getenv(testReportVar)
	if err := os.WriteFile(report+".tmp", []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		return 2
	}
	if err := os.Rename(report+".tmp", report); err != nil {
		return 2
	}
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(os.Getenv(testGoVar)); err == nil {
			return 0
		}
	}
	return 3
}

func holdLock(path string) int {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil || syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return 2
	}
	_, _ = os.Stdout.WriteString("locked\n")
	buf := make([]byte, 1)
	_, _ = os.Stdin.Read(buf) // until the test closes our stdin
	return 0
}

func waitFor(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(d); !ok(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func readReport(t *testing.T, path string) map[string]string {
	t.Helper()
	waitFor(t, 20*time.Second, "the helper's report", func() bool { _, err := os.Stat(path); return err == nil })
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, l := range strings.Split(string(data), "\n") {
		k, v, _ := strings.Cut(l, "=")
		got[k] = v
	}
	return got
}

func waitDone(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("helper ended with %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the helper did not end")
	}
}

func startChild(t *testing.T, root string) (dir string, report, goFile string) {
	t.Helper()
	report, goFile = filepath.Join(root, "report"), filepath.Join(root, "go")
	t.Setenv(testModeVar, "apply")
	t.Setenv(testReportVar, report)
	t.Setenv(testGoVar, goFile)
	t.Setenv(testDirVar, filepath.Join(root, "self-update"))
	t.Cleanup(func() { _ = os.WriteFile(goFile, nil, 0o600) }) // never leave a child waiting
	return filepath.Join(root, "self-update"), report, goFile
}

func TestAcquireLockRefusedWhileHeld(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "self-update")
	lock, err := AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "lock")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("lock file: %v, %v", fi, err)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("dir: %v, %v", fi, err)
	}
	if _, err := AcquireLock(dir); !errors.Is(err, ErrHelperRunning) {
		t.Errorf("second AcquireLock = %v, want ErrHelperRunning", err)
	}
	if st, _ := ReadState(dir); !st.Running {
		t.Error("ReadState says idle while the lock is held")
	}
	lock.Close()
	again, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("AcquireLock after release = %v", err)
	}
	again.Close()
}

func TestLaunchHandsTheLockToTheChild(t *testing.T) {
	dir, report, goFile := startChild(t, t.TempDir())
	lock, err := AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	bin, _ := filepath.Abs(os.Args[0])
	done, err := ExecLauncher{Binary: bin}.Launch("/some/plan.json", lock)
	lock.Close() // the caller lets go of its copy; the child keeps the lock
	if err != nil {
		t.Fatal(err)
	}
	got := readReport(t, report)
	if got["adopt"] != "ok" || got["held"] != "yes" {
		t.Errorf("the child did not adopt the lock: %v", got)
	}
	if got["childSeesLock"] != "no" {
		t.Errorf("a command of the child inherits the lock: %v", got)
	}
	if st, err := ReadState(dir); err != nil || !st.Running {
		t.Errorf("while the child runs: %+v, %v", st, err)
	}
	if _, err := AcquireLock(dir); !errors.Is(err, ErrHelperRunning) {
		t.Errorf("AcquireLock while the child runs = %v", err)
	}
	if err := os.WriteFile(goFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitDone(t, done)
	if st, err := ReadState(dir); err != nil || st.Running {
		t.Errorf("after the child ended: %+v, %v", st, err)
	}
}

func TestLaunchDetached(t *testing.T) {
	root := t.TempDir()
	dir, report, goFile := startChild(t, root)
	// a descriptor of this process without close-on-exec, as a listener handed down by a supervisor
	leak := filepath.Join(root, "listener")
	if err := os.WriteFile(leak, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	leakFd, err := syscall.Open(leak, syscall.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	bin, _ := filepath.Abs(os.Args[0])
	done, err := ExecLauncher{Binary: bin}.Launch("/some/plan.json", lock)
	syscall.Close(leakFd)
	if err != nil {
		t.Fatal(err)
	}
	got := readReport(t, report)
	if err := os.WriteFile(goFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitDone(t, done)
	if got["plan"] != "/some/plan.json" || got["cwd"] != "/" {
		t.Errorf("plan = %q, cwd = %q", got["plan"], got["cwd"])
	}
	if own, _ := sessionOf("self"); got["session"] == own || got["session"] == "" {
		t.Errorf("helper session %q, own %q", got["session"], own)
	}
	for _, fd := range []string{"fd0", "fd1", "fd2"} {
		if got[fd] != os.DevNull {
			t.Errorf("%s = %q, want %s", fd, got[fd], os.DevNull)
		}
	}
	for k, v := range got {
		if strings.HasPrefix(k, "fd") && strings.Contains(v, "listener") {
			t.Errorf("%s leaked: %s", k, v)
		}
	}
}

// A fd 3 that is not the lock of the directory is refused: another file, or no file handed on at all.
func TestAdoptLockRefusesTheWrongFd3(t *testing.T) {
	root := t.TempDir()
	dir, report, goFile := startChild(t, root)
	first, err := AcquireLock(dir) // makes <dir>/lock; the child compares against its inode
	if err != nil {
		t.Fatal(err)
	}
	first.Close()
	other, err := os.OpenFile(filepath.Join(root, "other"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := syscall.Flock(int(other.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	bin, _ := filepath.Abs(os.Args[0])
	done, err := ExecLauncher{Binary: bin}.Launch("/p", other)
	if err != nil {
		t.Fatal(err)
	}
	if got := readReport(t, report); got["adopt"] != "refused" {
		t.Errorf("another file as fd 3: %v", got)
	}
	_ = os.WriteFile(goFile, nil, 0o600)
	waitDone(t, done)

	// nothing handed on: started directly, fd 3 is whatever the runtime has there
	_ = os.Remove(goFile)
	_ = os.Remove(report)
	cmd := exec.Command(bin, "self-update-apply", "/p")
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if got := readReport(t, report); got["adopt"] != "refused" {
		t.Errorf("no fd 3: %v", got)
	}
	_ = os.WriteFile(goFile, nil, 0o600)
	if err := cmd.Wait(); err != nil {
		t.Error(err)
	}
}

// fd 3 is the right file, but another open file description holds the lock: refused, as the helper would otherwise
// take a lock that belongs to someone else.
func TestAdoptLockRefusesALockHeldElsewhere(t *testing.T) {
	root := t.TempDir()
	dir, report, goFile := startChild(t, root)
	held, err := AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	unlocked, err := os.Open(filepath.Join(dir, "lock")) // the right inode, a different open file description
	if err != nil {
		t.Fatal(err)
	}
	defer unlocked.Close()
	bin, _ := filepath.Abs(os.Args[0])
	done, err := ExecLauncher{Binary: bin}.Launch("/p", unlocked)
	if err != nil {
		t.Fatal(err)
	}
	got := readReport(t, report)
	if got["adopt"] != "refused" || !strings.Contains(got["adoptErr"], "not locked by this process") {
		t.Errorf("a lock held elsewhere: %v", got)
	}
	_ = os.WriteFile(goFile, nil, 0o600)
	waitDone(t, done)
	if st, _ := ReadState(dir); !st.Running {
		t.Error("the lock of the real holder was lost")
	}
}

func TestAcquireLockSurvivesProbes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "self-update")
	first, err := AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	first.Close()
	stop := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for {
			select {
			case <-stop:
				return
			default:
				_, _ = ReadState(dir)        // takes the lock for a moment
				time.Sleep(time.Millisecond) // a page polls far less often than this
			}
		}
	}()
	defer func() { close(stop); <-finished }()
	for i := 0; i < 200; i++ {
		l, err := AcquireLock(dir)
		if err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		l.Close()
	}
}

func TestLaunchNeedsTheLock(t *testing.T) {
	if _, err := (ExecLauncher{Binary: "/bin/true"}).Launch("/x", nil); err == nil {
		t.Error("Launch without a lock succeeded")
	}
}

// Where the write works, the cgroup file names another cgroup afterwards, and there is no error. Here cgroup.procs is
// the cgroup file itself, so the write replaces its content.
func TestEscapeCgroupMoved(t *testing.T) {
	root := t.TempDir()
	cgroups := filepath.Join(root, "cgroup")
	if err := os.WriteFile(cgroups, []byte("0::/openrc.edelweiss\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "edelweiss-selfupdate")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cgroups, filepath.Join(dir, "cgroup.procs")); err != nil {
		t.Fatal(err)
	}
	if err := escapeCgroup(cgroups, dir, 4242); err != nil {
		t.Errorf("escapeCgroup = %v, want no error", err)
	}
}

func TestEscapeCgroup(t *testing.T) {
	if procCgroup != "/proc/self/cgroup" || escapeDir != "/sys/fs/cgroup/edelweiss-selfupdate" || serviceCgroup != "openrc.edelweiss" {
		t.Errorf("procCgroup = %q, escapeDir = %q, serviceCgroup = %q", procCgroup, escapeDir, serviceCgroup)
	}
	for _, c := range []struct {
		name, cgroup string
		moves        bool
	}{
		{"the cgroup of edelweiss", "0::/openrc.edelweiss\n", true},
		{"below it", "0::/openrc.edelweiss/leaf\n", true},
		{"among others", "3:cpu:/user\n0::/openrc.edelweiss\n", true},
		{"the root cgroup", "0::/\n", false},
		{"another service", "0::/openrc.sshd\n", false},
		{"a session", "0::/user.slice/session-1.scope\n", false},
		{"nothing", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			cgroups := filepath.Join(root, "cgroup")
			if err := os.WriteFile(cgroups, []byte(c.cgroup), 0o644); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(root, "edelweiss-selfupdate")
			// the fixture does not change when the pid is written: a process that moved is seen to be still there
			want := error(nil)
			if c.moves {
				want = errStillInCgroup
			}
			if err := escapeCgroup(cgroups, dir, 4242); !errors.Is(err, want) {
				t.Errorf("escapeCgroup = %v, want %v", err, want)
			}
			procs, err := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
			if !c.moves {
				if err == nil {
					t.Errorf("the process moved: %q", procs)
				}
				if _, err := os.Stat(dir); err == nil {
					t.Error("the cgroup was made")
				}
				return
			}
			if err != nil || string(procs) != "4242" {
				t.Errorf("cgroup.procs = %q, %v; want the pid", procs, err)
			}
			if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o755 {
				t.Errorf("the cgroup directory: %v, %v", fi, err)
			}
		})
	}
	dir := filepath.Join(t.TempDir(), "edelweiss-selfupdate")
	if err := escapeCgroup(filepath.Join(t.TempDir(), "missing"), dir, 1); err != nil {
		t.Errorf("escapeCgroup without a cgroup file = %v, want no error", err)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("the cgroup was made without a cgroup file")
	}
}
