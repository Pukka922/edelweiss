package server

import (
	"os"
	"testing"

	"github.com/Pukka922/edelweiss/core/internal/testutil"
)

// TestMain removes the test extension binaries the testutil helpers built.
func TestMain(m *testing.M) {
	code := m.Run()
	testutil.Cleanup()
	os.Exit(code)
}
