package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Pukka922/edelweiss/core/internal/config"
	"github.com/Pukka922/edelweiss/core/internal/extmgr"
	"github.com/Pukka922/edelweiss/core/internal/testutil"
	"github.com/Pukka922/edelweiss/core/internal/version"
	"github.com/Pukka922/edelweiss/sdk/manifest"
)

func run(args ...string) (int, string, string) { return runIn("", args...) }

// runIn runs the CLI with stdin as its standard input.
func runIn(stdin string, args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := Main(args, strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestVersionAndUnknown(t *testing.T) {
	if code, out, _ := run("version"); code != 0 || out != version.Version+"\n" {
		t.Fatalf("version: %d %q", code, out)
	}
	if code, _, _ := run("bogus"); code != 2 {
		t.Fatalf("unknown command exit %d", code)
	}
}

// useExtDataRoot points the data directories of extensions at a temporary directory (never the real /var/lib) and
// returns it.
func useExtDataRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	setExtDataRoot(t, root)
	return root
}

// setExtDataRoot sets the data root of extensions for the duration of the test; root must lie in a temporary directory.
func setExtDataRoot(t *testing.T, root string) {
	t.Helper()
	old := extDataRoot
	extDataRoot = root
	t.Cleanup(func() { extDataRoot = old })
}

// installTestExt installs the test extension through the CLI.
func installTestExt(t *testing.T, cfgPath string) {
	t.Helper()
	bin, err := testutil.BuildTestExt()
	if err != nil {
		t.Fatal(err)
	}
	if code, out, errOut := run("ext", "install", bin, "--config", cfgPath); code != 0 {
		t.Fatalf("install: %d %q %q", code, out, errOut)
	}
}

func TestExtRemoveReportsTheDeletedDataDirectory(t *testing.T) {
	root := useExtDataRoot(t)
	cfgPath, _, _ := emptyStateConfig(t)
	bin, err := testutil.BuildTestExt()
	if err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(root, "edelweiss-ext-testext")
	other := filepath.Join(root, "edelweiss-ext-other")
	for _, d := range []string{data, other} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	install := func() {
		t.Helper()
		if code, out, errOut := run("ext", "install", bin, "--config", cfgPath); code != 0 {
			t.Fatalf("install: %d %q %q", code, out, errOut)
		}
	}

	install()
	code, out, errOut := run("ext", "remove", "testext", "--config", cfgPath)
	if want := "removed testext\ndeleted " + data + "\n"; code != 0 || out != want || errOut != "" {
		t.Fatalf("remove: %d %q %q, want %q", code, out, errOut, want)
	}
	if _, err := os.Stat(data); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the data directory is still there: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("the data directory of another extension was touched: %v", err)
	}

	// without a data directory only the removal is reported
	install()
	if code, out, errOut := run("ext", "remove", "testext", "--config", cfgPath); code != 0 || out != "removed testext\n" || errOut != "" {
		t.Fatalf("remove without data: %d %q %q", code, out, errOut)
	}
}

func TestExtRemoveReportsADeletedDanglingSymlink(t *testing.T) {
	root := useExtDataRoot(t)
	cfgPath, _, _ := emptyStateConfig(t)
	data := filepath.Join(root, "edelweiss-ext-testext")
	if err := os.Symlink(filepath.Join(root, "missing"), data); err != nil {
		t.Fatal(err)
	}
	installTestExt(t, cfgPath)

	// os.Stat would not see the link (it follows it into nothing): the link is deleted all the same and says so
	code, out, errOut := run("ext", "remove", "testext", "--config", cfgPath)
	if want := "removed testext\ndeleted " + data + "\n"; code != 0 || out != want || errOut != "" {
		t.Fatalf("remove: %d %q %q, want %q", code, out, errOut, want)
	}
	if _, err := os.Lstat(data); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the symlink is still there: %v", err)
	}
}

func TestExtRemoveWarnsWhenTheDataDirectoryCannotBeDeleted(t *testing.T) {
	// A data root that is a regular file: nothing below it can be deleted (ENOTDIR), whoever runs the test.
	root := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(root, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	setExtDataRoot(t, root)
	cfgPath, _, _ := emptyStateConfig(t)
	installTestExt(t, cfgPath)

	data := filepath.Join(root, "edelweiss-ext-testext")
	code, out, errOut := run("ext", "remove", "testext", "--config", cfgPath)
	if code != 1 || out != "removed testext\n" || !strings.HasPrefix(errOut, "warning: ") || !strings.Contains(errOut, data) ||
		!strings.Contains(errOut, "not a directory") || strings.Count(errOut, "\n") != 1 {
		t.Fatalf("remove: %d %q %q", code, out, errOut)
	}
	// the extension is gone all the same: there is nothing to retry
	if code, _, errOut := run("ext", "remove", "testext", "--config", cfgPath); code != 1 || !strings.Contains(errOut, "is not installed") {
		t.Fatalf("second remove: %d %q", code, errOut)
	}
}

func TestExtInstallListRemove(t *testing.T) {
	useExtDataRoot(t)
	state := testutil.TempDir(t)
	cfgPath := filepath.Join(state, "edelweiss.toml")
	cfg := "[paths]\nstate_dir = \"" + filepath.Join(state, "s") + "\"\nrun_dir = \"" + filepath.Join(state, "r") + "\"\n" +
		"\n[github]\napi_url = \"http://127.0.0.1:1\"\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	bin, err := testutil.BuildTestExt()
	if err != nil {
		t.Fatal(err)
	}

	if code, out, errOut := run("ext", "install", bin, "--config", cfgPath); code != 0 || !strings.Contains(out, "installed testext 1.0.0") {
		t.Fatalf("install: %d %q %q", code, out, errOut)
	}
	if code, out, _ := run("ext", "list", "--config", cfgPath); code != 0 || !strings.Contains(out, "testext") || !strings.Contains(out, "1.0.0") {
		t.Fatalf("list: %d %q", code, out)
	}
	if code, _, errOut := run("ext", "install", bin, "--id", "other", "--config", cfgPath); code != 1 || !strings.Contains(errOut, `does not match expected "other"`) {
		t.Fatalf("install with wrong id: %d %q", code, errOut)
	}
	if code, _, errOut := run("ext", "install", "github.com/foo/bar", "--config", cfgPath); code != 1 || !strings.Contains(errOut, "GitHub is unavailable") {
		t.Fatalf("github install: %d %q", code, errOut)
	}
	if code, _, _ := run("ext", "remove", "testext", "--config", cfgPath); code != 0 {
		t.Fatalf("remove: %d", code)
	}
	if code, _, _ := run("ext", "remove", "testext", "--config", cfgPath); code != 1 {
		t.Fatalf("second remove: %d", code)
	}
}

// emptyStateConfig writes a config whose state and run directories do not
// exist yet and returns its path and the two directories.
func emptyStateConfig(t *testing.T) (cfgPath, stateDir, runDir string) {
	t.Helper()
	base := t.TempDir()
	stateDir, runDir = filepath.Join(base, "state"), filepath.Join(base, "run")
	cfgPath = filepath.Join(base, "edelweiss.toml")
	cfg := "[paths]\nstate_dir = \"" + stateDir + "\"\nrun_dir = \"" + runDir + "\"\n" +
		"\n[github]\napi_url = \"http://127.0.0.1:1\"\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return cfgPath, stateDir, runDir
}

func assertNotCreated(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Fatalf("%s was created (stat err: %v)", d, err)
		}
	}
}

func TestServeRejectsPositionalArguments(t *testing.T) {
	// --config points at a missing file so that, were the arguments accepted,
	// the command would fail instead of starting a daemon.
	missing := filepath.Join(t.TempDir(), "missing.toml")
	code, _, errOut := run("serve", "--config", missing, "foo", "--dev")
	if code != 2 || !strings.Contains(errOut, `"foo"`) || !strings.Contains(errOut, "Usage:") {
		t.Fatalf("serve with stray argument: %d %q", code, errOut)
	}
}

func TestExtValidatesArgumentsBeforeTouchingDisk(t *testing.T) {
	cfgPath, state, runDir := emptyStateConfig(t)
	for _, args := range [][]string{
		{"ext"},
		{"ext", "bogus"},
		{"ext", "bogus", "--config", cfgPath},
		{"ext", "list", "extra", "--config", cfgPath},
		{"ext", "install", "--config", cfgPath},
		{"ext", "install", "a", "b", "--config", cfgPath},
		{"ext", "remove", "--config", cfgPath},
		{"ext", "remove", "a", "b", "--config", cfgPath},
		{"ext", "update", "--config", cfgPath},
		{"ext", "update", "a", "--all", "--config", cfgPath},
		{"ext", "update", "a", "b", "--config", cfgPath},
		{"ext", "install", "a", "--all", "--config", cfgPath},
	} {
		if code, _, errOut := run(args...); code != 2 || !strings.Contains(errOut, "Usage:") {
			t.Fatalf("%v: exit %d %q", args, code, errOut)
		}
		assertNotCreated(t, state, runDir)
	}
}

func TestExtListWithoutStore(t *testing.T) {
	cfgPath, state, runDir := emptyStateConfig(t)
	code, out, errOut := run("ext", "list", "--config", cfgPath)
	if code != 0 || strings.TrimSpace(out) != "ID  VERSION  SOURCE  NAME" || errOut != "" {
		t.Fatalf("list: %d %q %q", code, out, errOut)
	}
	assertNotCreated(t, state, runDir)
}

func TestExtInstallSourceErrors(t *testing.T) {
	cfgPath, state, runDir := emptyStateConfig(t)
	dir := t.TempDir()
	for _, tc := range []struct {
		source string
		want   string
		not    string
	}{
		{"github.com/foo/bar", "GitHub is unavailable", ""},
		{"https://github.com/foo/bar", "GitHub is unavailable", ""},
		{"foo/bar@v1.2.3", "GitHub is unavailable", ""},
		{"github.com/foo", "not a GitHub repository", "unavailable"},
		{filepath.Join(dir, "missing"), "no such file or directory", "GitHub"},
		{"missing-extension", "no such file or directory", "GitHub"},
		{"./foo/bar/baz", "no such file or directory", "GitHub"},
		{"./bar", "no such file or directory", "GitHub"},
		{dir, "not a regular file", "GitHub"},
	} {
		code, _, errOut := run("ext", "install", tc.source, "--config", cfgPath)
		if code != 1 || !strings.Contains(errOut, tc.want) || (tc.not != "" && strings.Contains(errOut, tc.not)) {
			t.Fatalf("install %q: exit %d, stderr %q (want %q)", tc.source, code, errOut, tc.want)
		}
		assertNotCreated(t, state, runDir)
	}
}

// githubConfig writes a config whose state lives in a temp dir and whose GitHub API is api.
func githubConfig(t *testing.T, api string) string {
	t.Helper()
	base := testutil.TempDir(t)
	cfgPath := filepath.Join(base, "edelweiss.toml")
	cfg := "[paths]\nstate_dir = \"" + filepath.Join(base, "s") + "\"\nrun_dir = \"" + filepath.Join(base, "r") + "\"\n" +
		"[github]\napi_url = \"" + api + "\"\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return cfgPath
}

func binary(t *testing.T, version string) []byte {
	t.Helper()
	p, err := testutil.BuildTestExtVersion(version)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func release(rs *testutil.ReleaseServer, tag, version string, bin []byte) {
	files := map[string][]byte{testutil.AssetName("testext", version): bin}
	files["checksums.txt"] = testutil.Checksums(files)
	rs.Add("acme/tools", tag, files)
}

func TestExtInstallFromGitHub(t *testing.T) {
	rs := testutil.NewReleaseServer(t)
	bin := binary(t, "")
	release(rs, "v1.0.0", "1.0.0", bin)
	cfgPath := githubConfig(t, rs.URL)

	code, out, errOut := runIn("n\n", "ext", "install", "acme/tools", "--config", cfgPath)
	if code != 1 || !strings.Contains(errOut, "aborted") {
		t.Fatalf("declined: %d %q %q", code, out, errOut)
	}
	for _, want := range []string{"acme/tools", "v1.0.0", "testext 1.0.0", testutil.AssetName("testext", "1.0.0"),
		testutil.SHA256(bin), "Warning", "Install testext 1.0.0? [y/N]"} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary lacks %q:\n%s", want, out)
		}
	}
	if _, out, _ := run("ext", "list", "--config", cfgPath); strings.Contains(out, "testext") {
		t.Fatalf("installed although declined:\n%s", out)
	}

	code, out, errOut = runIn("yes\n", "ext", "install", "https://github.com/acme/tools@v1.0.0", "--config", cfgPath)
	if code != 0 || !strings.Contains(out, "installed testext 1.0.0 from acme/tools v1.0.0") {
		t.Fatalf("install: %d %q %q", code, out, errOut)
	}
	if _, out, _ := run("ext", "list", "--config", cfgPath); !strings.Contains(out, "github:github.com/acme/tools") {
		t.Fatalf("list:\n%s", out)
	}
	if code, _, errOut := run("ext", "install", "acme/tools", "--id", "other", "--yes", "--config", cfgPath); code != 1 || !strings.Contains(errOut, `no extension with this id "other"`) {
		t.Fatalf("unknown id: %d %q", code, errOut)
	}
}

func TestExtUpdate(t *testing.T) {
	rs := testutil.NewReleaseServer(t)
	release(rs, "v1.0.0", "1.0.0", binary(t, ""))
	cfgPath := githubConfig(t, rs.URL)
	if code, _, errOut := run("ext", "install", "acme/tools", "--yes", "--config", cfgPath); code != 0 {
		t.Fatalf("install: %q", errOut)
	}

	if code, out, _ := run("ext", "update", "--all", "--config", cfgPath); code != 0 || !strings.Contains(out, "all extensions are up to date") {
		t.Fatalf("nothing to update: %d %q", code, out)
	}
	if code, out, _ := run("ext", "update", "testext", "--config", cfgPath); code != 0 || !strings.Contains(out, "testext is up to date") {
		t.Fatalf("single up to date: %d %q", code, out)
	}

	bin11 := binary(t, "1.1.0")
	release(rs, "v1.1.0", "1.1.0", bin11)
	code, out, errOut := runIn("n\n", "ext", "update", "--all", "--config", cfgPath)
	if code != 1 || !strings.Contains(out, "testext  1.0.0      1.1.0      acme/tools v1.1.0") || !strings.Contains(errOut, "aborted") {
		t.Fatalf("declined update: %d %q %q", code, out, errOut)
	}
	for _, want := range []string{"ASSET", "SHA-256", testutil.AssetName("testext", "1.1.0"), testutil.SHA256(bin11)} {
		if !strings.Contains(out, want) {
			t.Fatalf("update list lacks %q:\n%s", want, out)
		}
	}
	warning := strings.Index(out, "Warning: ")
	if prompt := strings.Index(out, "Update 1 extension(s)? [y/N] "); warning < 0 || prompt < warning ||
		!strings.Contains(out, "Only install extensions from sources you trust.") {
		t.Fatalf("no warning before the prompt:\n%s", out)
	}
	if _, out, _ := run("ext", "list", "--config", cfgPath); strings.Contains(out, "1.1.0") {
		t.Fatalf("updated although declined:\n%s", out)
	}
	code, out, errOut = run("ext", "update", "testext", "--yes", "--config", cfgPath)
	if code != 0 || !strings.Contains(out, "updated testext 1.0.0 → 1.1.0") || !strings.Contains(out, testutil.SHA256(bin11)) ||
		!strings.Contains(out, "Warning: ") || strings.Contains(out, "[y/N]") {
		t.Fatalf("update: %d %q %q", code, out, errOut)
	}
	if _, out, _ := run("ext", "list", "--config", cfgPath); !strings.Contains(out, "1.1.0") {
		t.Fatalf("list after update:\n%s", out)
	}
	if code, _, errOut := run("ext", "update", "nope", "--config", cfgPath); code != 1 || !strings.Contains(errOut, `"nope" is not installed`) {
		t.Fatalf("unknown: %d %q", code, errOut)
	}
}

// changingReader is a stdin that runs change when it is first read: the release changes while the user is looking
// at the summary and has not answered yet.
type changingReader struct {
	*strings.Reader
	change func()
	once   sync.Once
}

func (r *changingReader) Read(p []byte) (int, error) {
	r.once.Do(r.change)
	return r.Reader.Read(p)
}

// runChanging runs the CLI with stdin "y" and runs change when the confirmation prompt reads it.
func runChanging(cfgPath string, change func(), args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	in := &changingReader{Reader: strings.NewReader("y\n"), change: change}
	code := Main(append(args, "--config", cfgPath), in, &out, &errOut)
	return code, out.String(), errOut.String()
}

// TestExtChangedReleaseIsNotInstalled proves that what is installed or updated is the file that was shown: if the
// release lists another checksum by the time the user confirms, nothing is installed.
func TestExtChangedReleaseIsNotInstalled(t *testing.T) {
	rs := testutil.NewReleaseServer(t)
	bin := binary(t, "")
	release(rs, "v1.0.0", "1.0.0", bin)
	cfgPath := githubConfig(t, rs.URL)
	other := func(version string, bin []byte) func() { // same tag and file name, other content
		return func() { release(rs, "v"+version, version, append(bytes.Clone(bin), 0)) }
	}

	code, out, errOut := runChanging(cfgPath, other("1.0.0", bin), "ext", "install", "acme/tools")
	if code != 1 || !strings.Contains(out, testutil.SHA256(bin)) || !strings.Contains(errOut, "the release changed since it was reviewed") {
		t.Fatalf("install of a changed release: %d %q %q", code, out, errOut)
	}
	if _, out, _ := run("ext", "list", "--config", cfgPath); strings.Contains(out, "testext") {
		t.Fatalf("installed although the release changed:\n%s", out)
	}

	release(rs, "v1.0.0", "1.0.0", bin)
	if code, _, errOut := run("ext", "install", "acme/tools", "--yes", "--config", cfgPath); code != 0 {
		t.Fatalf("install: %q", errOut)
	}
	bin11 := binary(t, "1.1.0")
	release(rs, "v1.1.0", "1.1.0", bin11)
	code, out, errOut = runChanging(cfgPath, other("1.1.0", bin11), "ext", "update", "--all")
	if code != 1 || !strings.Contains(out, "1.1.0") || !strings.Contains(errOut, "update of testext failed") ||
		!strings.Contains(errOut, "the release changed since it was reviewed") {
		t.Fatalf("update to a changed release: %d %q %q", code, out, errOut)
	}
	if _, out, _ := run("ext", "list", "--config", cfgPath); strings.Contains(out, "1.1.0") {
		t.Fatalf("updated although the release changed:\n%s", out)
	}
}

// TestExtInstallPinsTheConfirmedRelease: without @tag the install is bound to the release that was shown, so a
// release published while the user reads the summary is neither installed nor a reason to fail.
func TestExtInstallPinsTheConfirmedRelease(t *testing.T) {
	rs := testutil.NewReleaseServer(t)
	release(rs, "v1.0.0", "1.0.0", binary(t, ""))
	bin11 := binary(t, "1.1.0")
	cfgPath := githubConfig(t, rs.URL)

	code, out, errOut := runChanging(cfgPath, func() { release(rs, "v1.1.0", "1.1.0", bin11) }, "ext", "install", "acme/tools")
	if code != 0 || !strings.Contains(out, "installed testext 1.0.0 from acme/tools v1.0.0") {
		t.Fatalf("install: %d %q %q", code, out, errOut)
	}
}

// An empty stdin (EOF) at the prompt is "no": nothing is installed and "aborted" does not end up on the prompt line.
func TestExtInstallEOFAtPromptDeclines(t *testing.T) {
	rs := testutil.NewReleaseServer(t)
	release(rs, "v1.0.0", "1.0.0", binary(t, ""))
	cfgPath := githubConfig(t, rs.URL)

	code, out, errOut := run("ext", "install", "acme/tools", "--config", cfgPath)
	if code != 1 || errOut != "aborted\n" || !strings.HasSuffix(out, "Install testext 1.0.0? [y/N] \n") {
		t.Fatalf("EOF at the prompt: %d %q %q", code, out, errOut)
	}
	if _, out, _ := run("ext", "list", "--config", cfgPath); strings.Contains(out, "testext") {
		t.Fatalf("installed without an answer:\n%s", out)
	}
}

func TestExtUpdateOfFileInstall(t *testing.T) {
	cfgPath := githubConfig(t, "http://127.0.0.1:1")
	bin, err := testutil.BuildTestExt()
	if err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := run("ext", "install", bin, "--config", cfgPath); code != 0 {
		t.Fatalf("install: %q", errOut)
	}
	if code, out, errOut := run("ext", "update", "testext", "--yes", "--config", cfgPath); code != 1 ||
		!strings.Contains(errOut, `"testext" was not installed from GitHub`) || out != "" {
		t.Fatalf("update of a file install: %d %q %q", code, out, errOut)
	}
}

func TestServiceArguments(t *testing.T) {
	for _, args := range [][]string{{"service"}, {"service", "bogus"}, {"service", "install", "extra"}, {"service", "uninstall", "--bogus"}, {"service", "install", "--purge"}} {
		if code, _, errOut := run(args...); code != 2 || !strings.Contains(errOut, "Usage:") {
			t.Fatalf("%v: exit %d %q", args, code, errOut)
		}
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: the root check cannot be tested without touching the system")
	}
	for _, args := range [][]string{{"service", "install"}, {"service", "uninstall", "--purge"}} {
		if code, _, errOut := run(args...); code != 1 || !strings.Contains(errOut, "must be run as root") {
			t.Fatalf("non-root %v: %d %q", args, code, errOut)
		}
	}
}

func TestExtInstallNamesMissingPackages(t *testing.T) {
	state := testutil.TempDir(t)
	cfgPath := filepath.Join(state, "edelweiss.toml")
	cfg := "[paths]\nstate_dir = \"" + filepath.Join(state, "s") + "\"\nrun_dir = \"" + filepath.Join(state, "r") + "\"\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(state, "needs-pkg")
	m := `{"id":"needspkg","version":"1.0.0","apiVersion":1,"name":{"en":"Needs"},"menu":{"section":"tools"},` +
		`"requires":{"apk":["edelweiss-test-missing"]}}`
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho '"+m+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// the package exists nowhere: missing on Alpine (make test-alpine) and without an apk database (the host)
	code, _, errOut := run("ext", "install", script, "--config", cfgPath)
	want := "install failed: missing packages: edelweiss-test-missing\ninstall them as root first: apk add edelweiss-test-missing\n"
	if code != 1 || errOut != want {
		t.Fatalf("install: %d %q, want %q", code, errOut, want)
	}
	if _, out, _ := run("ext", "list", "--config", cfgPath); strings.Contains(out, "needspkg") {
		t.Fatalf("listed although refused: %q", out)
	}
}

func TestReportFailure(t *testing.T) {
	var b bytes.Buffer
	reportFailure(&b, "update of docker", fmt.Errorf("x: %w", &extmgr.MissingPackagesError{Packages: []string{"docker", "cmd:sh"}}))
	if want := "update of docker failed: x: missing packages: docker, cmd:sh\ninstall them as root first: apk add docker cmd:sh\n"; b.String() != want {
		t.Fatalf("got %q, want %q", b.String(), want)
	}
	b.Reset()
	reportFailure(&b, "install", errors.New("boom"))
	if b.String() != "install failed: boom\n" {
		t.Fatalf("got %q", b.String())
	}
}

func TestPrintExtensionsNamesMissingPackages(t *testing.T) {
	exts := []*extmgr.Extension{
		{Manifest: &manifest.Manifest{ID: "docker", Version: "0.1.0", Name: manifest.Localized{"en": "Docker"}},
			Source: extmgr.Source{Type: "github", Location: "github.com/Pukka922/edelweiss-ext-docker"}},
		{Manifest: &manifest.Manifest{ID: "system", Version: "0.1.0", Name: manifest.Localized{"en": "Overview"}},
			Source: extmgr.Source{Type: "file", Location: "/tmp/system"}},
	}
	var out bytes.Buffer
	printExtensions(&out, exts, func(e *extmgr.Extension) []string {
		if e.Manifest.ID == "docker" {
			return []string{"docker", "cmd:docker-compose"}
		}
		return []string{}
	})
	s := out.String()
	if !strings.HasPrefix(s, "ID") || !strings.Contains(s, "github:github.com/Pukka922/edelweiss-ext-docker") ||
		!strings.HasSuffix(s, "docker: missing packages: docker, cmd:docker-compose; install them as root: apk add docker cmd:docker-compose\n") ||
		strings.Contains(s, "system: missing") {
		t.Fatalf("output:\n%s", s)
	}
}

// stubPackages stands in for apk's database.
type stubPackages struct {
	missing []string
	err     error
}

func (s stubPackages) Missing([]string) ([]string, error) { return s.missing, s.err }

// TestExtListNamesWhyPackagesCannotBeChecked: with an apk database that cannot be read, every required package
// counts as missing, and stderr says why; stderr stays empty when the packages are installed or merely missing.
func TestExtListNamesWhyPackagesCannotBeChecked(t *testing.T) {
	state := testutil.TempDir(t)
	cfg := config.Default()
	cfg.Paths.StateDir = filepath.Join(state, "s")
	cfg.Paths.RunDir = filepath.Join(state, "r")
	script := filepath.Join(state, "needs-pkg")
	m := `{"id":"needspkg","version":"1.0.0","apiVersion":1,"name":{"en":"Needs"},"menu":{"section":"tools"},` +
		`"requires":{"apk":["edelweiss-test-missing"]}}`
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho '"+m+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	q, err := extmgr.NewExecQuerier()
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := extmgr.New(extmgr.Options{Dir: cfg.ExtensionsDir(), TmpDir: cfg.TmpDir(), Querier: q, Packages: stubPackages{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.InstallFile(context.Background(), script, extmgr.Source{Type: "file", Location: script}, ""); err != nil {
		t.Fatal(err)
	}

	list := func(p extmgr.PackageChecker) (string, string) {
		var out, errOut bytes.Buffer
		if code := listExtensions(cfg, p, &out, &errOut); code != 0 {
			t.Fatalf("list: %d %q", code, errOut.String())
		}
		return out.String(), errOut.String()
	}
	const missingLine = "needspkg: missing packages: edelweiss-test-missing; install them as root: apk add edelweiss-test-missing\n"

	out, errOut := list(stubPackages{err: errors.New("read /lib/apk/db/installed: permission denied")})
	if !strings.HasSuffix(out, missingLine) {
		t.Errorf("unreadable database, output:\n%s", out)
	}
	for _, want := range []string{"level=WARN", `msg="cannot check the required packages"`, "id=needspkg", "permission denied"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("unreadable database, stderr lacks %q:\n%s", want, errOut)
		}
	}

	out, errOut = list(stubPackages{missing: []string{"edelweiss-test-missing"}})
	if !strings.HasSuffix(out, missingLine) || errOut != "" {
		t.Errorf("package not installed: output:\n%s\nstderr: %q", out, errOut)
	}
	out, errOut = list(stubPackages{})
	if strings.Contains(out, "missing packages") || errOut != "" {
		t.Errorf("everything installed: output:\n%s\nstderr: %q", out, errOut)
	}
}
