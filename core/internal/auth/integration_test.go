//go:build integration

package auth

import (
	"errors"
	"slices"
	"testing"

	"github.com/Pukka922/edelweiss/core/internal/testutil"
)

func TestRealShadowAndGroups(t *testing.T) {
	testutil.EnsureUser(t, "edeltest", "Edel-Pass-1", "edelgrp")
	v, err := NewVerifier("/etc/shadow")
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Verify("edeltest", "Edel-Pass-1"); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := v.Verify("edeltest", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password: %v", err)
	}
	u, err := OSUserDB{}.Lookup("edeltest")
	if err != nil || !slices.Contains(u.Groups, "edelgrp") {
		t.Fatalf("lookup: %+v %v", u, err)
	}
}
