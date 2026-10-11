package auth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeShadow(t *testing.T) string {
	t.Helper()
	return writeShadowContent(t, shadowFixture)
}

func writeShadowContent(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "shadow")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestVerify(t *testing.T) {
	v, err := NewVerifier(writeShadow(t))
	if err != nil {
		t.Fatal(err)
	}
	v.Now = func() time.Time { return time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC) }

	for _, name := range []string{"alice", "md5user", "sha256user", "yescryptuser", "bcryptuser"} {
		if err := v.Verify(name, "edelweiss-test"); err != nil {
			t.Errorf("%s with correct password: %v", name, err)
		}
		if err := v.Verify(name, "wrong"); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("%s with wrong password: %v", name, err)
		}
	}
	for _, name := range []string{"root", "locked", "nologin", "expired", "nobody-here"} {
		if err := v.Verify(name, "edelweiss-test"); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("%s: expected ErrInvalidCredentials, got %v", name, err)
		}
	}
}

func TestVerifyMissingShadowIsOperationalError(t *testing.T) {
	v, err := NewVerifier(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatal(err)
	}
	err = v.Verify("alice", "x")
	if err == nil || errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected operational error, got %v", err)
	}
}

const sha512Vector = "$6$Gx2pQr7sLm9tVw1z$nIdtw29mh8NZeWN5kYZQLBWb6sqwVSUcBmKfBiHmUkzZCjM2ZYEV6evPEf0UhG6fpFVTqemVFZDBMtL4bXs1.."

func TestVerifyAccountExpiry(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 34, 56, 0, time.UTC)
	today := now.Unix() / 86400

	line := func(name, expire string) string {
		return fmt.Sprintf("%s:%s:20000:0:99999:7::%s:\n", name, sha512Vector, expire)
	}
	shadow := line("future", fmt.Sprint(today+1)) +
		line("today", fmt.Sprint(today)) +
		line("past", fmt.Sprint(today-1)) +
		line("malformed", "abc") +
		line("never", "")

	v, err := NewVerifier(writeShadowContent(t, shadow))
	if err != nil {
		t.Fatal(err)
	}
	v.Now = func() time.Time { return now }

	for _, name := range []string{"future", "never"} {
		if err := v.Verify(name, "edelweiss-test"); err != nil {
			t.Errorf("%s should still be valid: %v", name, err)
		}
	}
	for _, name := range []string{"today", "past", "malformed"} {
		if err := v.Verify(name, "edelweiss-test"); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("%s should be expired, got %v", name, err)
		}
	}
}

func TestVerifyUndecodableHashIsOperationalError(t *testing.T) {
	shadow := "scryptuser:$7$CU..../....$Zm9vYmFyYmF6:20000::::::\n" +
		"garbage:not-a-hash:20000::::::\n" +
		"alice:" + sha512Vector + ":20000::::::\n"
	v, err := NewVerifier(writeShadowContent(t, shadow))
	if err != nil {
		t.Fatal(err)
	}
	v.Now = func() time.Time { return time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC) }

	for _, name := range []string{"scryptuser", "garbage"} {
		err := v.Verify(name, "edelweiss-test")
		if err == nil || errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("%s: expected operational error, got %v", name, err)
			continue
		}
		if !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "unsupported or invalid password hash") {
			t.Errorf("%s: unhelpful error %q", name, err)
		}
	}
	// A plain mismatch on a decodable hash stays a credential failure.
	if err := v.Verify("alice", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("alice with wrong password: %v", err)
	}
}

func TestDummyHashIsDecodableAndNotThePublicTestPassword(t *testing.T) {
	v, err := NewVerifier(writeShadow(t))
	if err != nil {
		t.Fatal(err)
	}
	match, err := v.match(dummyHash, "edelweiss-test")
	if err != nil {
		t.Fatalf("fallback dummy hash must decode so that it costs real work: %v", err)
	}
	if match {
		t.Fatal("fallback dummy hash must not match the public test password")
	}
}

func TestDummyFor(t *testing.T) {
	entries := map[string]ShadowEntry{
		"alice":   {Name: "alice", Hash: "$y$alice", Expire: -1},
		"bob":     {Name: "bob", Hash: "$5$bob", Expire: -1},
		"carol":   {Name: "carol", Hash: "$6$carol", Expire: -1},
		"empty":   {Name: "empty", Hash: "", Expire: -1},
		"locked":  {Name: "locked", Hash: "!$6$locked", Expire: -1},
		"nologin": {Name: "nologin", Hash: "*", Expire: -1},
	}

	tests := []struct {
		name string
		want string
	}{
		{"nobody-here", "$y$alice"}, // smallest usable name
		{"carol", "$y$alice"},
		{"alice", "$5$bob"}, // never the requested user's own entry
		{"locked", "$y$alice"},
		{"nologin", "$y$alice"},
		{"empty", "$y$alice"},
	}
	for _, tc := range tests {
		// Map iteration order is random, so repeat to prove determinism.
		for i := 0; i < 50; i++ {
			if got := dummyFor(entries, tc.name); got != tc.want {
				t.Fatalf("dummyFor(%q) = %q, want %q", tc.name, got, tc.want)
			}
		}
	}
}

func TestDummyForFallsBackToConstant(t *testing.T) {
	cases := map[string]map[string]ShadowEntry{
		"empty map": {},
		"only unusable entries": {
			"empty":   {Name: "empty", Hash: ""},
			"locked":  {Name: "locked", Hash: "!$6$locked"},
			"nologin": {Name: "nologin", Hash: "*"},
		},
		"only the requested user is usable": {
			"alice":  {Name: "alice", Hash: "$6$alice"},
			"locked": {Name: "locked", Hash: "!"},
		},
	}
	for label, entries := range cases {
		if got := dummyFor(entries, "alice"); got != dummyHash {
			t.Errorf("%s: dummyFor = %q, want fallback constant", label, got)
		}
	}
}
