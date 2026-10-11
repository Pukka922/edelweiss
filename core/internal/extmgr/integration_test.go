//go:build integration

package extmgr

import (
	"context"
	"os/user"
	"strconv"
	"testing"

	"github.com/Pukka922/edelweiss/core/internal/testutil"
)

func TestQueryAndInstallRunAsNobody(t *testing.T) {
	testutil.RequireIntegration(t)
	q := newQuerier(t)
	nobody, err := user.Lookup("nobody")
	if err != nil {
		t.Fatal(err)
	}
	if q.Credential == nil ||
		strconv.Itoa(int(q.Credential.Uid)) != nobody.Uid || strconv.Itoa(int(q.Credential.Gid)) != nobody.Gid {
		t.Fatalf("querier credential %+v, want nobody (uid %s, gid %s)", q.Credential, nobody.Uid, nobody.Gid)
	}
	m, _ := newManager(t, testutil.TempDir(t), nil)
	if _, err := m.InstallFile(context.Background(), testExt(t), Source{Type: "file"}, ""); err != nil {
		t.Fatalf("install as root with nobody query: %v", err)
	}
}
