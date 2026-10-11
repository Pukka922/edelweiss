//go:build integration

package apkdb

import (
	"slices"
	"testing"

	"github.com/Pukka922/edelweiss/core/internal/testutil"
)

// The real database of the Alpine test image: musl is always installed, busybox-binsh provides cmd:sh.
func TestRealDatabase(t *testing.T) {
	testutil.RequireIntegration(t)
	missing, err := New(DefaultPath).Missing([]string{"musl", "cmd:sh", "edelweiss-test-missing"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(missing, []string{"edelweiss-test-missing"}) {
		t.Fatalf("Missing = %v", missing)
	}
}
