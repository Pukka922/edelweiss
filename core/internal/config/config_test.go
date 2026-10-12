package config

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "edelweiss.toml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaultIsValid(t *testing.T) {
	c := Default()
	if err := c.Validate(); err != nil {
		t.Fatalf("default config invalid: %v", err)
	}
	if c.Listen != "0.0.0.0:9090" || !c.TLS.Enabled || c.Auth.AdminGroup != "wheel" {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if c.ExtensionsDir() != "/var/lib/edelweiss/extensions" || c.SocketDir() != "/run/edelweiss/sockets" ||
		c.TmpDir() != "/var/lib/edelweiss/tmp" || c.TLSDir() != "/var/lib/edelweiss/tls" {
		t.Fatal("unexpected derived paths")
	}
}

func TestLoadOverrides(t *testing.T) {
	p := writeConfig(t, `
listen = "127.0.0.1:9443"
[tls]
enabled = false
[auth]
admin_group = "admins"
session_idle_timeout = "5m"
[extensions]
max_download_size = "1GB"
[paths]
state_dir = "/tmp/ew/state"
[[dev.extensions]]
binary = "/work/dist/ext"
ui_dir = "/work/ui/dist"
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:9443" || c.TLS.Enabled || c.Auth.AdminGroup != "admins" {
		t.Fatalf("overrides not applied: %+v", c)
	}
	if c.Auth.SessionIdleTimeout.Duration != 5*time.Minute || c.Auth.SessionMaxAge.Duration != 12*time.Hour {
		t.Fatalf("durations: %v %v", c.Auth.SessionIdleTimeout, c.Auth.SessionMaxAge)
	}
	if c.Extensions.MaxDownloadSize != 1<<30 {
		t.Fatalf("max download %d", c.Extensions.MaxDownloadSize)
	}
	if c.ExtensionsDir() != "/tmp/ew/state/extensions" {
		t.Fatalf("extensions dir %s", c.ExtensionsDir())
	}
	if len(c.Dev.Extensions) != 1 || c.Dev.Extensions[0].UIDir != "/work/ui/dist" {
		t.Fatalf("dev extensions %+v", c.Dev.Extensions)
	}
	if len(c.Auth.DenyUsers) != 1 || c.Auth.DenyUsers[0] != "root" {
		t.Fatalf("deny users default lost: %v", c.Auth.DenyUsers)
	}
}

func TestLoadRejects(t *testing.T) {
	cases := map[string]string{
		"unknown key":      `listn = "x"`,
		"cert without key": "[tls]\ncert = \"/a.pem\"",
		"bad duration":     "[auth]\nsession_idle_timeout = \"soon\"",
		"relative path":    "[paths]\nstate_dir = \"state\"",
		"bad listen":       `listen = "9090"`,
		"bad level":        "[log]\nlevel = \"loud\"",
		"empty port":       `listen = "0.0.0.0:"`,
		"non-numeric port": `listen = "0.0.0.0:abc"`,
		"dev flag in file": "[dev]\nenabled = true", // only serve --dev switches the development mode on
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, content)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestLoadUnknownKeyNamesKey(t *testing.T) {
	_, err := Load(writeConfig(t, `listn = "x"`))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "listn") {
		t.Fatalf("error does not name the unknown key: %v", err)
	}
	if !strings.Contains(err.Error(), "config ") {
		t.Fatalf("error lost the config path prefix: %v", err)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseByteSize(t *testing.T) {
	cases := map[string]int64{"100": 100, "1KB": 1024, "2MiB": 2 << 20, "200MB": 200 << 20, "1gb": 1 << 30, " 3 MB ": 3 << 20, "9223372036854775807": math.MaxInt64}
	for in, want := range cases {
		got, err := ParseByteSize(in)
		if err != nil || got != want {
			t.Errorf("ParseByteSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "MB", "-1MB", "1TB", "17179869185GB", "8589934592GB", "9223372036854775807KB"} {
		if _, err := ParseByteSize(in); err == nil {
			t.Errorf("ParseByteSize(%q) expected error", in)
		}
	}
}

func TestEnsureDirs(t *testing.T) {
	for _, existing := range []bool{false, true} {
		base := t.TempDir()
		c := Default()
		c.Paths.StateDir = filepath.Join(base, "state")
		c.Paths.RunDir = filepath.Join(base, "run")
		if existing { // directories with wrong modes must be corrected, not just left alone
			for _, d := range []string{c.Paths.StateDir, c.Paths.RunDir} {
				if err := os.Mkdir(d, 0o700); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := c.EnsureDirs(); err != nil {
			t.Fatal(err)
		}
		for p, want := range map[string]os.FileMode{c.Paths.StateDir: 0o711, c.Paths.RunDir: 0o755} {
			fi, err := os.Stat(p)
			if err != nil || fi.Mode().Perm() != want {
				t.Fatalf("existing=%v %s: %v %v", existing, p, fi, err)
			}
		}
	}
}
