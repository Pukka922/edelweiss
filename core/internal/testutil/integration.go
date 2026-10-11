//go:build integration

package testutil

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// RequireIntegration skips unless EDELWEISS_INTEGRATION=1 is set, and fails if
// it is set without root: the integration tests run as root inside the
// throwaway Alpine container of `make test-alpine`, anything else is a
// misconfiguration that must not turn into a silent skip.
func RequireIntegration(t testing.TB) {
	t.Helper()
	if os.Getenv("EDELWEISS_INTEGRATION") != "1" {
		t.Skip("integration test: set EDELWEISS_INTEGRATION=1 and run as root (make test-alpine)")
	}
	if os.Geteuid() != 0 {
		t.Fatalf("EDELWEISS_INTEGRATION=1 but euid is %d: integration tests need root (make test-alpine)", os.Geteuid())
	}
}

// lockAccounts serializes account changes across test binaries: `go test ./...`
// runs packages in parallel, and busybox adduser/addgroup/chpasswd rewrite the
// shared /etc/passwd, /etc/group and /etc/shadow without locking against each
// other. The returned func releases the lock.
func lockAccounts(t testing.TB) func() {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(os.TempDir(), "edelweiss-accounts.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		t.Fatal(err)
	}
	return func() { f.Close() } // closing the descriptor drops the flock
}

// EnsureUser creates a local user with password and supplementary groups (busybox tools).
func EnsureUser(t testing.TB, name, password string, groups ...string) *user.User {
	t.Helper()
	RequireIntegration(t)
	defer lockAccounts(t)()
	if _, err := user.Lookup(name); err != nil {
		runCmd(t, nil, "adduser", "-D", "-s", "/bin/sh", name)
	}
	for _, g := range groups {
		if _, err := user.LookupGroup(g); err != nil {
			runCmd(t, nil, "addgroup", g)
		}
		_ = exec.Command("addgroup", name, g).Run() // fails harmlessly if already a member
	}
	runCmd(t, strings.NewReader(name+":"+password+"\n"), "chpasswd")
	u, err := user.Lookup(name)
	if err != nil {
		t.Fatal(err)
	}
	// The addgroup error above is ignored (it fails for existing members), so
	// check the outcome instead: every requested group must now list the user.
	ids, err := u.GroupIds()
	if err != nil {
		t.Fatalf("groups of %s: %v", name, err)
	}
	for _, g := range groups {
		gr, err := user.LookupGroup(g)
		if err != nil {
			t.Fatalf("lookup group %s: %v", g, err)
		}
		if !slices.Contains(ids, gr.Gid) {
			t.Fatalf("user %s is not a member of group %s (gid %s): has gids %v", name, g, gr.Gid, ids)
		}
	}
	return u
}

func runCmd(t testing.TB, stdin *strings.Reader, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}
