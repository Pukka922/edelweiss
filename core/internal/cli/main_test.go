package cli

import (
	"fmt"
	"os"
	"testing"

	"github.com/Pukka922/edelweiss/core/internal/testutil"
)

// TestMain points the data directories of extensions at a temporary directory, so that no test can reach the real
// /var/lib (`ext remove` deletes <extDataRoot>/edelweiss-ext-<id>), and removes the test extension binaries the
// testutil helpers built.
func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "edelweiss-cli-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	extDataRoot = root
	code := m.Run()
	_ = os.RemoveAll(root)
	testutil.Cleanup()
	os.Exit(code)
}
