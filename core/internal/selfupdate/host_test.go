package selfupdate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Pukka922/edelweiss/core/internal/service"
)

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestDetectMode(t *testing.T) {
	cases := []struct {
		name, mounts string
		want         Mode
	}{
		{"sys", "/dev/sda2 / ext4 rw 0 0\ntmpfs /run tmpfs rw 0 0\n", Sys},
		{"no root line", "proc /proc proc rw 0 0\n", Sys},
		{"diskless", "tmpfs / tmpfs rw 0 0\nproc /proc proc rw 0 0\n", Diskless},
		{"diskless with tmpfs var", "tmpfs / tmpfs rw 0 0\ntmpfs /var tmpfs rw 0 0\n", Diskless},
		{"data", "tmpfs / tmpfs rw 0 0\n/dev/mmcblk0p3 /var ext4 rw 0 0\n", Data},
		{"data, octal escapes", "tmpfs / tmpfs rw 0 0\n" + `/dev/disk/by-label/my\040disk /var ext4 rw 0 0` + "\n", Data},
		{"escaped mount point", "tmpfs / tmpfs rw 0 0\n" + `/dev/sdb1 /va\162 ext4 rw 0 0` + "\n", Data},
		{"last mount wins", "tmpfs / tmpfs rw 0 0\n/dev/sdb1 /var ext4 rw 0 0\ntmpfs /var tmpfs rw 0 0\n", Diskless},
		{"root remounted on disk", "tmpfs / tmpfs rw 0 0\n/dev/sda1 / ext4 rw 0 0\n", Sys},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "proc/mounts"), c.mounts, 0o644)
			got, err := DetectMode(root)
			if err != nil || got != c.want {
				t.Fatalf("DetectMode = %q, %v; want %q", got, err, c.want)
			}
		})
	}
	if _, err := DetectMode(t.TempDir()); err == nil {
		t.Fatal("DetectMode without proc/mounts: want an error")
	}
}

func TestLbuIncludes(t *testing.T) {
	list := "# comment\n+etc/edelweiss\n+usr/local\n-usr/local/skip\n+usr/local/skip/back\n\n+opt/tool\n-opt/tool\n"
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "etc/apk/protected_paths.d/lbu.list"), list, 0o644)
	cases := []struct {
		path string
		want bool
	}{
		{"/etc/edelweiss", true},
		{"/etc/edelweiss/edelweiss.toml", true},
		{"/usr/local/bin/edelweissd", true},
		{"/usr/local/skip/x", false},
		{"/usr/local/skip/back/x", true},
		{"/opt/tool/x", false},
		{"/etc/other", false},
		{"/etc/edelweiss-extra", false},
		{"/var/lib/x", false},
	}
	for _, c := range cases {
		got, err := LbuIncludes(root, c.path)
		if err != nil || got != c.want {
			t.Errorf("LbuIncludes(%q) = %v, %v; want %v", c.path, got, err, c.want)
		}
	}
	got, err := LbuIncludes(t.TempDir(), "/etc/x")
	if err != nil || got {
		t.Fatalf("missing list: got %v, %v; want false, nil", got, err)
	}
}

func TestFindTools(t *testing.T) {
	root := t.TempDir()
	if got := FindTools(root); got != (Tools{}) {
		t.Fatalf("empty root: %+v", got)
	}
	writeFile(t, filepath.Join(root, "sbin/rc-service"), "", 0o755)
	writeFile(t, filepath.Join(root, "usr/bin/lbu"), "", 0o755)
	writeFile(t, filepath.Join(root, "sbin/lbu"), "", 0o755) // earlier directory wins
	if got := FindTools(root); got != (Tools{RcService: "/sbin/rc-service", Lbu: "/usr/bin/lbu"}) {
		t.Fatalf("FindTools = %+v", got)
	}
	// not executable: not found
	root2 := t.TempDir()
	writeFile(t, filepath.Join(root2, "usr/sbin/rc-service"), "", 0o644)
	if got := FindTools(root2); got.RcService != "" {
		t.Fatalf("non-executable counted: %+v", got)
	}
}

func eligibleHost(t *testing.T) Host {
	t.Helper()
	root := t.TempDir()
	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(binDir, "edelweissd")
	script, err := service.Script(binary)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "usr/sbin/rc-service"), "", 0o755)
	writeFile(t, filepath.Join(root, "etc/init.d/edelweiss"), script, 0o755)
	writeFile(t, filepath.Join(root, "proc/42/comm"), "supervise-daemon\n", 0o644)
	return Host{Root: root, Binary: binary, Arch: "arm64", Version: "0.2.0", PPID: 42}
}

// The kernel truncates comm to 15 characters, so the parent of a real service shows as supervise-daemo.
func TestEligibleTruncatedParentName(t *testing.T) {
	h := eligibleHost(t)
	writeFile(t, filepath.Join(h.Root, "proc/42/comm"), "supervise-daemo\n", 0o644)
	if got := Eligible(h); got != ReasonNone {
		t.Fatalf("a parent named as the kernel shows supervise-daemon: %q", got)
	}
}

func TestEligible(t *testing.T) {
	if got := Eligible(eligibleHost(t)); got != ReasonNone {
		t.Fatalf("eligible host: %q", got)
	}
	cases := []struct {
		name   string
		mutate func(h *Host)
		want   Reason
	}{
		{"dev mode", func(h *Host) { h.Dev = true }, ReasonDevMode},
		{"dev mode before dev build", func(h *Host) { h.Dev = true; h.Version = "0.0.0-dev" }, ReasonDevMode},
		{"dev build", func(h *Host) { h.Version = "0.0.0-dev" }, ReasonDevBuild},
		{"no semver", func(h *Host) { h.Version = "abc" }, ReasonDevBuild},
		{"empty version", func(h *Host) { h.Version = "" }, ReasonDevBuild},
		{"dev build before arch", func(h *Host) { h.Version = "0.0.0-dev"; h.Arch = "riscv64" }, ReasonDevBuild},
		{"arch", func(h *Host) { h.Arch = "riscv64" }, ReasonArch},
		{"arch before openrc", func(h *Host) { h.Arch = "386"; _ = os.Remove(filepath.Join(h.Root, "usr/sbin/rc-service")) }, ReasonArch},
		{"no openrc", func(h *Host) { _ = os.Remove(filepath.Join(h.Root, "usr/sbin/rc-service")) }, ReasonNoOpenRC},
		{"no init script", func(h *Host) { _ = os.Remove(filepath.Join(h.Root, "etc/init.d/edelweiss")) }, ReasonNotService},
		{"no supervisor", func(h *Host) {
			writeFile(t, filepath.Join(h.Root, "etc/init.d/edelweiss"), "command=\""+h.Binary+"\"\n", 0o755)
		}, ReasonNotService},
		{"other command", func(h *Host) {
			writeFile(t, filepath.Join(h.Root, "etc/init.d/edelweiss"), "supervisor=supervise-daemon\ncommand=\"/usr/local/bin/other\"\n", 0o755)
		}, ReasonNotService},
		{"parent is not the supervisor", func(h *Host) {
			writeFile(t, filepath.Join(h.Root, "proc/42/comm"), "bash\n", 0o644)
		}, ReasonNotService},
		{"parent is another truncated name", func(h *Host) {
			writeFile(t, filepath.Join(h.Root, "proc/42/comm"), "supervise-daem\n", 0o644)
		}, ReasonNotService},
		{"no parent comm", func(h *Host) { h.PPID = 43 }, ReasonNotService},
		{"not writable", func(h *Host) { h.Binary = filepath.Join(filepath.Dir(h.Binary), "gone", "edelweissd") }, ReasonNotWritable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := eligibleHost(t)
			c.mutate(&h)
			if c.want == ReasonNotWritable {
				// the init script must still name the (moved) binary so that only the directory check fails
				script, err := service.Script(h.Binary)
				if err != nil {
					t.Fatal(err)
				}
				writeFile(t, filepath.Join(h.Root, "etc/init.d/edelweiss"), script, 0o755)
			}
			if got := Eligible(h); got != c.want {
				t.Fatalf("Eligible = %q; want %q", got, c.want)
			}
		})
	}
	t.Run("amd64", func(t *testing.T) {
		h := eligibleHost(t)
		h.Arch = "amd64"
		if got := Eligible(h); got != ReasonNone {
			t.Fatalf("Eligible = %q", got)
		}
	})
}
