//go:build integration

package procman

import (
	"context"
	"encoding/json"
	"net/http"
	"os/user"
	"slices"
	"strconv"
	"testing"

	"github.com/Pukka922/edelweiss/core/internal/auth"
	"github.com/Pukka922/edelweiss/core/internal/testutil"
)

// helloAs starts (or reuses) the extension process for spec and returns the OS
// uid and supplementary groups it reports. A missing osUid, a non-200 status or
// an undecodable body fails the test instead of reading as uid 0.
func helloAs(t *testing.T, m *Manager, spec Spec) (uid int, groups []int) {
	t.Helper()
	p, release, err := m.Acquire(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	resp := get(t, p, "/hello")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("hello: status %d", resp.StatusCode)
	}
	var h struct {
		OSUid  *int  `json:"osUid"`
		Groups []int `json:"groups"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		t.Fatalf("decode hello: %v", err)
	}
	if h.OSUid == nil {
		t.Fatal("hello response has no osUid")
	}
	return *h.OSUid, h.Groups
}

func TestSpawnAsUserWithGroupsAndAsRoot(t *testing.T) {
	testutil.EnsureUser(t, "edeltest", "Edel-Pass-1", "edelgrp")
	u, err := auth.OSUserDB{}.Lookup("edeltest")
	if err != nil {
		t.Fatal(err)
	}
	grp, err := user.LookupGroup("edelgrp")
	if err != nil {
		t.Fatal(err)
	}
	grpID, err := strconv.Atoi(grp.Gid)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(u.GroupIDs, grpID) {
		t.Fatalf("edeltest has gids %v, want it to include edelgrp (%d)", u.GroupIDs, grpID)
	}

	m, _ := newTestManager(t)
	spec := testSpec(t)
	spec.Key.User = "edeltest"
	spec.Identity = Identity{Name: u.Name, UID: u.UID, GID: u.GID, GroupIDs: u.GroupIDs, Home: u.Home}
	uid, groups := helloAs(t, m, spec)
	if uid != u.UID {
		t.Fatalf("uid %d, want %d", uid, u.UID)
	}
	if !slices.Contains(groups, grpID) {
		t.Fatalf("process groups %v miss the supplementary group edelgrp (%d)", groups, grpID)
	}
	for _, g := range u.GroupIDs {
		if !slices.Contains(groups, g) {
			t.Fatalf("groups %v missing %d", groups, g)
		}
	}

	root, err := auth.OSUserDB{}.Lookup("root")
	if err != nil {
		t.Fatal(err)
	}
	adminSpec := spec
	adminSpec.Key.Admin = true
	adminSpec.Identity = Identity{Name: root.Name, UID: root.UID, GID: root.GID, GroupIDs: root.GroupIDs, Home: root.Home}
	if uid, _ := helloAs(t, m, adminSpec); uid != 0 {
		t.Fatalf("admin uid %d", uid)
	}
}
