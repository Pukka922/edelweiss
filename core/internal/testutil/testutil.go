// Package testutil contains helpers shared by core tests.
package testutil

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

var (
	buildOnce sync.Once
	buildPath string
	buildErr  error

	versionMu    sync.Mutex
	versionPaths = map[string]string{}

	dirsMu sync.Mutex
	dirs   []string
)

// buildDir creates a world-readable temp dir for a built test extension and records it for Cleanup.
func buildDir() (string, error) {
	dir, err := os.MkdirTemp("", "edelweiss-testext-")
	if err != nil {
		return "", err
	}
	dirsMu.Lock()
	dirs = append(dirs, dir)
	dirsMu.Unlock()
	if err := os.Chmod(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// Cleanup removes every directory BuildTestExt and BuildTestExtVersion created in this process and resets their
// caches. Every package that uses the helpers calls it from a TestMain after m.Run, so no built binary (about
// 12 MB each) outlives the test binary.
func Cleanup() {
	versionMu.Lock()
	defer versionMu.Unlock()
	dirsMu.Lock()
	defer dirsMu.Unlock()
	for _, d := range dirs {
		_ = os.RemoveAll(d)
	}
	dirs = nil
	versionPaths = map[string]string{}
	buildOnce = sync.Once{}
	buildPath, buildErr = "", nil
}

// BuildTestExt compiles core/internal/testext once per test binary and returns
// the path of the executable (mode 0755, in a world-readable temp dir that Cleanup removes).
func BuildTestExt() (string, error) {
	buildOnce.Do(func() {
		dir, err := buildDir()
		if err != nil {
			buildErr = err
			return
		}
		buildPath = filepath.Join(dir, "testext")
		cmd := exec.Command("go", "build", "-o", buildPath, "github.com/Pukka922/edelweiss/core/internal/testext")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("build testext: %v\n%s", err, out)
		}
	})
	return buildPath, buildErr
}

// BuildTestExtVersion compiles testext with its manifest version replaced by version, once per version and test
// binary. "" is BuildTestExt.
func BuildTestExtVersion(version string) (string, error) {
	if version == "" {
		return BuildTestExt()
	}
	versionMu.Lock()
	defer versionMu.Unlock()
	if p, ok := versionPaths[version]; ok {
		return p, nil
	}
	dir, err := buildDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, "testext")
	cmd := exec.Command("go", "build", "-ldflags", "-X main.manifestVersion="+version, "-o", p,
		"github.com/Pukka922/edelweiss/core/internal/testext")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build testext %s: %v\n%s", version, err, out)
	}
	versionPaths[version] = p
	return p, nil
}
