package extmgr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Pukka922/edelweiss/core/internal/config"
	"github.com/Pukka922/edelweiss/core/internal/testutil"
	"github.com/Pukka922/edelweiss/sdk/manifest"
)

// fakePackages answers every check with missing and err and records what was asked.
type fakePackages struct {
	missing []string
	err     error
	asked   [][]string
}

func (f *fakePackages) Missing(names []string) ([]string, error) {
	f.asked = append(f.asked, slices.Clone(names))
	return f.missing, f.err
}

func newPackagesManager(t *testing.T, state string, pk PackageChecker, dev []config.DevExtension) *Manager {
	t.Helper()
	m, err := New(Options{Dir: filepath.Join(state, "extensions"), TmpDir: filepath.Join(state, "tmp"),
		Querier: newQuerier(t), Packages: pk, Dev: dev})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// scriptExt writes a script that prints manifest as its --edelweiss-manifest answer.
func scriptExt(t *testing.T, dir, name, manifestJSON string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\necho '"+manifestJSON+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

const dockerManifest = `{"id":"docker","version":"%s","apiVersion":1,"name":{"en":"Docker"},"menu":{"section":"containers"}%s}`

func dockerScript(t *testing.T, dir, version, requires string) string {
	t.Helper()
	extra := ""
	if requires != "" {
		extra = `,"requires":{"apk":[` + requires + `]}`
	}
	return scriptExt(t, dir, "docker-"+version, fmt.Sprintf(dockerManifest, version, extra))
}

func TestInstallRefusesMissingPackages(t *testing.T) {
	state := testutil.TempDir(t)
	pk := &fakePackages{missing: []string{"docker"}}
	m := newPackagesManager(t, state, pk, nil)
	bin := dockerScript(t, state, "0.1.0", `"docker","cmd:sh"`)

	_, err := m.InstallFile(context.Background(), bin, Source{Type: "file"}, "")
	var mp *MissingPackagesError
	if !errors.Is(err, ErrMissingPackages) || !errors.As(err, &mp) || !slices.Equal(mp.Packages, []string{"docker"}) {
		t.Fatalf("err = %v, want a MissingPackagesError for docker", err)
	}
	if err.Error() != "missing packages: docker" {
		t.Fatalf("message %q", err.Error())
	}
	if len(pk.asked) != 1 || !slices.Equal(pk.asked[0], []string{"docker", "cmd:sh"}) {
		t.Fatalf("asked %v", pk.asked)
	}
	if len(m.List()) != 0 {
		t.Fatal("extension installed despite missing packages")
	}
	if _, err := os.Stat(filepath.Join(state, "extensions", "docker")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("store directory exists: %v", err)
	}
	if left, _ := os.ReadDir(filepath.Join(state, "tmp")); len(left) != 0 {
		t.Fatalf("temp leftovers: %v", left)
	}
}

func TestInstallWithInstalledPackages(t *testing.T) {
	state := testutil.TempDir(t)
	pk := &fakePackages{}
	m := newPackagesManager(t, state, pk, nil)
	ext, err := m.InstallFile(context.Background(), dockerScript(t, state, "0.1.0", `"docker"`), Source{Type: "file"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ext.Manifest.RequiredAPK(), []string{"docker"}) || len(pk.asked) != 1 {
		t.Fatalf("manifest %v, asked %v", ext.Manifest.Requires, pk.asked)
	}
}

func TestInstallWithoutRequirementsSkipsTheCheck(t *testing.T) {
	state := testutil.TempDir(t)
	pk := &fakePackages{err: errors.New("must not be asked")}
	m := newPackagesManager(t, state, pk, nil)
	if _, err := m.InstallFile(context.Background(), dockerScript(t, state, "0.1.0", ""), Source{Type: "file"}, ""); err != nil {
		t.Fatal(err)
	}
	if len(pk.asked) != 0 {
		t.Fatalf("asked %v", pk.asked)
	}
}

func TestUpdateWithMissingPackagesKeepsTheInstalledVersion(t *testing.T) {
	state := testutil.TempDir(t)
	pk := &fakePackages{}
	m := newPackagesManager(t, state, pk, nil)
	ctx := context.Background()
	if _, err := m.InstallFile(ctx, dockerScript(t, state, "0.1.0", ""), Source{Type: "file"}, ""); err != nil {
		t.Fatal(err)
	}
	pk.missing = []string{"docker"}
	if _, err := m.InstallFile(ctx, dockerScript(t, state, "0.2.0", `"docker"`), Source{Type: "file"}, ""); !errors.Is(err, ErrMissingPackages) {
		t.Fatalf("err = %v", err)
	}
	if e, ok := m.Get("docker"); !ok || e.Manifest.Version != "0.1.0" {
		t.Fatalf("installed: %v %v", ok, e)
	}
}

func TestInstallWhenThePackageCheckFails(t *testing.T) {
	state := testutil.TempDir(t)
	m := newPackagesManager(t, state, &fakePackages{err: errors.New("apk database: is a directory")}, nil)
	_, err := m.InstallFile(context.Background(), dockerScript(t, state, "0.1.0", `"docker"`), Source{Type: "file"}, "")
	if err == nil || errors.Is(err, ErrMissingPackages) || errors.Is(err, ErrInvalidExtension) ||
		!strings.Contains(err.Error(), "cannot check the required packages") {
		t.Fatalf("err = %v", err)
	}
	if len(m.List()) != 0 {
		t.Fatal("installed although the check failed")
	}
}

func TestMissingPackages(t *testing.T) {
	pk := &fakePackages{missing: []string{"docker"}}
	m := newPackagesManager(t, testutil.TempDir(t), pk, nil)
	needs := &Extension{Manifest: &manifest.Manifest{ID: "docker",
		Requires: &manifest.Requires{APK: []string{"docker", "cmd:sh"}}}}
	if got := m.MissingPackages(needs); !slices.Equal(got, []string{"docker"}) {
		t.Fatalf("MissingPackages = %v", got)
	}
	plain := &Extension{Manifest: &manifest.Manifest{ID: "system"}}
	if got := m.MissingPackages(plain); got == nil || len(got) != 0 {
		t.Fatalf("MissingPackages without requirements = %#v, want an empty non-nil slice", got)
	}
	pk.missing = nil
	if got := m.MissingPackages(needs); got == nil || len(got) != 0 {
		t.Fatalf("MissingPackages when all are installed = %#v, want an empty non-nil slice", got)
	}
	pk.err = errors.New("apk database: permission denied")
	if got := m.MissingPackages(needs); !slices.Equal(got, []string{"docker", "cmd:sh"}) {
		t.Fatalf("MissingPackages on a database error = %v, want all required", got)
	}
}

func TestDevExtensionWithMissingPackagesLoads(t *testing.T) {
	state := testutil.TempDir(t)
	pk := &fakePackages{missing: []string{"docker"}}
	bin := dockerScript(t, state, "0.1.0", `"docker"`)
	m := newPackagesManager(t, state, pk, []config.DevExtension{{Binary: bin}})
	if err := m.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	e, ok := m.Get("docker")
	if !ok || !e.Dev {
		t.Fatalf("dev extension not loaded: %v %v", ok, e)
	}
	if got := m.MissingPackages(e); !slices.Equal(got, []string{"docker"}) {
		t.Fatalf("MissingPackages = %v", got)
	}
}
