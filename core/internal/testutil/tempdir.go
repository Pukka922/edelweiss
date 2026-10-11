package testutil

import (
	"os"
	"path/filepath"
	"testing"
)

// TempDir returns t.TempDir() made traversable (0711) for other users, so
// processes running as nobody or a test user can reach files below it.
func TempDir(t testing.TB) string {
	t.Helper()
	d := t.TempDir()
	for _, p := range []string{filepath.Dir(d), d} {
		if err := os.Chmod(p, 0o711); err != nil {
			t.Fatal(err)
		}
	}
	return d
}
