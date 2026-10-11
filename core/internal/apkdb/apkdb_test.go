package apkdb

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// sample is a shortened apk database: blocks separated by empty lines, the last one without a trailing newline.
const sample = `C:Q1abc=
P:musl
V:1.2.6-r2
p:so:libc.musl-x86_64.so.1=1

P:busybox-binsh
V:1.37.0-r31
p:/bin/sh cmd:sh=1.37.0-r31

P:docker-engine
V:29.1.2-r0
p:cmd:dockerd=29.1.2-r0 cmd:docker-proxy`

func TestParse(t *testing.T) {
	names, err := Parse(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"musl", "busybox-binsh", "docker-engine", "so:libc.musl-x86_64.so.1", "/bin/sh", "cmd:sh",
		"cmd:dockerd", "cmd:docker-proxy"} {
		if !names[n] {
			t.Errorf("%q not found", n)
		}
	}
	for _, n := range []string{"1.37.0-r31", "Q1abc=", "docker", "cmd:sh=1.37.0-r31"} {
		if names[n] {
			t.Errorf("%q must not count as installed", n)
		}
	}
}

func TestParseLongLine(t *testing.T) {
	long := "P:big\np:" + strings.Repeat("cmd:x ", 50000) + "cmd:last\n"
	names, err := Parse(strings.NewReader(long))
	if err != nil {
		t.Fatal(err)
	}
	if !names["big"] || !names["cmd:last"] {
		t.Fatal("entries of a long provides line are missing")
	}
}

func writeDB(t *testing.T, path, content string, mod time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func TestMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "installed")
	writeDB(t, path, sample, time.Now())
	db := New(path)
	missing, err := db.Missing([]string{"docker", "musl", "cmd:sh", "edelweiss-test-missing"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(missing, []string{"docker", "edelweiss-test-missing"}) {
		t.Fatalf("Missing = %v", missing)
	}
	if missing, err := db.Missing([]string{"musl", "cmd:dockerd"}); err != nil || missing != nil {
		t.Fatalf("Missing of installed names = %v, %v; want nil, nil", missing, err)
	}
}

func TestMissingWithoutDatabase(t *testing.T) {
	db := New(filepath.Join(t.TempDir(), "no-such-file"))
	missing, err := db.Missing([]string{"musl", "docker"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(missing, []string{"musl", "docker"}) {
		t.Fatalf("Missing without a database = %v, want every name", missing)
	}
}

func TestMissingReportsReadErrors(t *testing.T) {
	db := New(t.TempDir()) // a directory: it exists, but reading it fails
	if _, err := db.Missing([]string{"musl"}); err == nil {
		t.Fatal("want an error for an unreadable database")
	}
	if missing, err := db.Missing(nil); err != nil || missing != nil {
		t.Fatalf("Missing(nil) = %v, %v; it must not read the database", missing, err)
	}
}

func TestMissingRereadsAChangedDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "installed")
	old := time.Now().Add(-time.Hour)
	writeDB(t, path, "P:musl\n", old)
	db := New(path)
	if missing, _ := db.Missing([]string{"docker"}); !slices.Equal(missing, []string{"docker"}) {
		t.Fatalf("before apk add: %v", missing)
	}
	// what apk add does while edelweissd runs: the file gets new content and a new modification time
	writeDB(t, path, "P:musl\n\nP:docker\n", time.Now())
	if missing, err := db.Missing([]string{"docker"}); err != nil || missing != nil {
		t.Fatalf("after apk add: %v, %v", missing, err)
	}
}
