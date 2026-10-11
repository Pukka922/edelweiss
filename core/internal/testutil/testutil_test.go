package testutil

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	code := m.Run()
	Cleanup()
	os.Exit(code)
}

func TestBuildTestExtPrintsManifest(t *testing.T) {
	bin, err := BuildTestExt()
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bin, "--edelweiss-manifest").Output()
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		ID      string `json:"id"`
		IconSVG string `json:"iconSvg"`
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("invalid manifest output %q: %v", out, err)
	}
	if m.ID != "testext" || m.IconSVG == "" {
		t.Fatalf("unexpected manifest %+v", m)
	}
}

func TestCleanupRemovesBuildDirs(t *testing.T) {
	plain, err := BuildTestExt()
	if err != nil {
		t.Fatal(err)
	}
	versioned, err := BuildTestExtVersion("9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{plain, versioned} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("built binary missing: %v", err)
		}
	}

	Cleanup()

	for _, p := range []string{plain, versioned} {
		if _, err := os.Stat(filepath.Dir(p)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("build dir %s survives Cleanup (stat error %v)", filepath.Dir(p), err)
		}
	}

	// The per-process caches are reset, so the helpers build fresh binaries again.
	again, err := BuildTestExt()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(again); err != nil {
		t.Fatalf("rebuilt binary missing: %v", err)
	}
}
