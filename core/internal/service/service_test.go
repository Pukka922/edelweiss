package service

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Pukka922/edelweiss/core/internal/config"
)

type recorder struct {
	calls []string
	fail  map[string]error // command line → error
}

func (r *recorder) run(name string, args ...string) error {
	line := strings.Join(append([]string{name}, args...), " ")
	r.calls = append(r.calls, line)
	return r.fail[line]
}

func newOptions(t *testing.T) (Options, *recorder, *bytes.Buffer) {
	t.Helper()
	rec := &recorder{fail: map[string]error{}}
	out := &bytes.Buffer{}
	return Options{Root: t.TempDir(), Binary: "/usr/local/sbin/edelweissd", Run: rec.run, Out: out}, rec, out
}

func read(t *testing.T, o Options, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(o.Root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func mode(t *testing.T, o Options, rel string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(filepath.Join(o.Root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func exists(o Options, rel string) bool {
	_, err := os.Stat(filepath.Join(o.Root, rel))
	return err == nil
}

func TestInstall(t *testing.T) {
	o, rec, out := newOptions(t)
	if err := Install(o); err != nil {
		t.Fatal(err)
	}
	script := read(t, o, InitScript)
	for _, want := range []string{
		"#!/sbin/openrc-run", "supervisor=supervise-daemon", `command="/usr/local/sbin/edelweissd"`,
		`command_args="serve --config ${EDELWEISS_CONFIG:-/etc/edelweiss/edelweiss.toml} ${EDELWEISS_ARGS}"`,
		"respawn_delay=2", "respawn_max=5", `retry="TERM/30/KILL/5"`,
		`output_logger="logger -t edelweiss"`, `error_logger="logger -t edelweiss"`, "use net logger", "after firewall",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("init script lacks %q:\n%s", want, script)
		}
	}
	if !strings.Contains(read(t, o, ConfD), `rc_ulimit="-n 65536"`) {
		t.Error("conf.d lacks the file descriptor limit")
	}
	if read(t, o, ConfigFile) != DefaultConfig {
		t.Error("config is not the default")
	}
	for rel, want := range map[string]os.FileMode{InitScript: 0o755, ConfD: 0o644, ConfigFile: 0o600, ConfigDir: 0o755, StateDir: 0o711} {
		if got := mode(t, o, rel); got != want {
			t.Errorf("mode of %s = %o, want %o", rel, got, want)
		}
	}
	if !slices.Equal(rec.calls, []string{"rc-update add edelweiss default"}) {
		t.Fatalf("commands %v", rec.calls)
	}
	if !strings.Contains(out.String(), "wrote /etc/init.d/edelweiss") || !strings.Contains(out.String(), "rc-service edelweiss start") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestDefaultConfigIsTheBuiltInDefault(t *testing.T) {
	p := filepath.Join(t.TempDir(), "edelweiss.toml")
	if err := os.WriteFile(p, []byte(DefaultConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg, config.Default()) {
		t.Fatalf("DefaultConfig differs from config.Default():\n%+v\n%+v", cfg, config.Default())
	}
}

func TestReinstallKeepsSettingsAndReplacesTheScript(t *testing.T) {
	o, _, out := newOptions(t)
	for rel, content := range map[string]string{InitScript: "old script", ConfD: "EDELWEISS_ARGS=--dev\n", ConfigFile: "listen = \"127.0.0.1:9443\"\n"} {
		p := filepath.Join(o.Root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := Install(o); err != nil {
		t.Fatal(err)
	}
	if read(t, o, InitScript) == "old script" {
		t.Error("init script not replaced")
	}
	if read(t, o, ConfD) != "EDELWEISS_ARGS=--dev\n" || read(t, o, ConfigFile) != "listen = \"127.0.0.1:9443\"\n" {
		t.Error("settings overwritten")
	}
	for _, want := range []string{"kept existing /etc/conf.d/edelweiss", "kept existing /etc/edelweiss/edelweiss.toml"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestInstallRejectsUnsafeBinaryPaths(t *testing.T) {
	for _, bin := range []string{"", "relative/edelweissd", `/opt/edel"weiss`, "/opt/$(id)", "/opt/with space", "/opt/a`b`"} {
		o, rec, _ := newOptions(t)
		o.Binary = bin
		if err := Install(o); err == nil {
			t.Errorf("accepted %q", bin)
		}
		if exists(o, InitScript) || len(rec.calls) != 0 {
			t.Errorf("%q: wrote files or ran commands", bin)
		}
	}
}

func TestInstallReportsRcUpdateFailure(t *testing.T) {
	o, rec, _ := newOptions(t)
	rec.fail["rc-update add edelweiss default"] = errors.New("exit status 1")
	if err := Install(o); err == nil || !strings.Contains(err.Error(), "rc-update add edelweiss default") {
		t.Fatalf("got %v", err)
	}
}

func TestUninstall(t *testing.T) {
	o, rec, _ := newOptions(t)
	if err := Install(o); err != nil {
		t.Fatal(err)
	}
	const tuned = "EDELWEISS_ARGS=--dev\n" // an administrator edited the conf.d file
	if err := os.WriteFile(filepath.Join(o.Root, ConfD), []byte(tuned), 0o644); err != nil {
		t.Fatal(err)
	}
	rec.calls = nil
	if err := Uninstall(o, false); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rec.calls, []string{"rc-service --ifstarted edelweiss stop", "rc-update del edelweiss default"}) {
		t.Fatalf("commands %v", rec.calls)
	}
	if exists(o, InitScript) {
		t.Fatal("uninstall left the init script")
	}
	if !exists(o, ConfD) || !exists(o, ConfigFile) || !exists(o, StateDir) {
		t.Fatal("uninstall without purge must keep the conf.d file, the configuration and the state")
	}
	if got := read(t, o, ConfD); got != tuned {
		t.Fatalf("uninstall without purge changed the conf.d file: %q", got)
	}

	// A reinstall reuses the kept conf.d file.
	if err := Install(o); err != nil {
		t.Fatal(err)
	}
	if got := read(t, o, ConfD); got != tuned {
		t.Fatalf("reinstall changed the kept conf.d file: %q", got)
	}
	if err := Uninstall(o, true); err != nil {
		t.Fatal(err)
	}
	if exists(o, InitScript) || exists(o, ConfD) || exists(o, ConfigDir) || exists(o, StateDir) {
		t.Fatal("purge kept the init script, the conf.d file, the configuration or the state")
	}
}

// After an uninstall without --purge only the settings remain; a later "uninstall --purge" removes them, too.
func TestPurgeAfterUninstallRemovesTheKeptSettings(t *testing.T) {
	o, rec, _ := newOptions(t)
	if err := Install(o); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall(o, false); err != nil {
		t.Fatal(err)
	}
	rec.calls = nil
	if err := Uninstall(o, true); err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("the init script is gone, so OpenRC is not called: %v", rec.calls)
	}
	if exists(o, ConfD) || exists(o, ConfigDir) || exists(o, StateDir) {
		t.Fatal("purge kept the conf.d file, the configuration or the state")
	}
}

func TestUninstallEdgeCases(t *testing.T) {
	o, rec, _ := newOptions(t)
	if err := Uninstall(o, false); err != nil || len(rec.calls) != 0 {
		t.Fatalf("nothing installed: %v %v", err, rec.calls)
	}

	o, rec, out := newOptions(t)
	if err := Install(o); err != nil {
		t.Fatal(err)
	}
	rec.fail["rc-update del edelweiss default"] = errors.New("exit status 1") // not in the runlevel: only a warning
	if err := Uninstall(o, false); err != nil || exists(o, InitScript) || !strings.Contains(out.String(), "warning: rc-update del") {
		t.Fatalf("rc-update del failure: %v\n%s", err, out)
	}

	o, rec, _ = newOptions(t)
	if err := Install(o); err != nil {
		t.Fatal(err)
	}
	rec.fail["rc-service --ifstarted edelweiss stop"] = errors.New("exit status 1")
	if err := Uninstall(o, true); err == nil || !exists(o, InitScript) || !exists(o, StateDir) {
		t.Fatalf("a failed stop must leave everything in place: %v", err)
	}
}

func TestInstallKeepsTheModeOfAnExistingConfigDir(t *testing.T) {
	o, _, _ := newOptions(t)
	dir := filepath.Join(o.Root, ConfigDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { // an administrator tightened it
		t.Fatal(err)
	}
	if err := Install(o); err != nil {
		t.Fatal(err)
	}
	if got := mode(t, o, ConfigDir); got != 0o700 {
		t.Errorf("mode of %s = %o, want 700", ConfigDir, got)
	}
}

func TestInstallWithoutOpenRCWritesNothing(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no rc-update
	o := Options{Root: t.TempDir(), Binary: "/usr/local/sbin/edelweissd"}
	if err := Install(o); err == nil || !strings.Contains(err.Error(), "rc-update was not found") {
		t.Fatalf("got %v", err)
	}
	if entries, err := os.ReadDir(o.Root); err != nil || len(entries) != 0 {
		t.Fatalf("wrote into the root: %v %v", entries, err)
	}
}

func TestFailedWriteLeavesNoTempFile(t *testing.T) {
	o, _, _ := newOptions(t)
	// A directory where the init script belongs: the temp file is written, then the rename fails.
	if err := os.MkdirAll(filepath.Join(o.Root, InitScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Install(o); err == nil {
		t.Fatal("install over a directory succeeded")
	}
	if exists(o, InitScript+".new") {
		t.Error("temp file left behind")
	}
}
