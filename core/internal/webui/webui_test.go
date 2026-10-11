package webui

import (
	"io/fs"
	"os"
	"testing"
)

func TestFSUsesDevDirWhenSet(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/index.html", []byte("dev"), 0o644); err != nil {
		t.Fatal(err)
	}
	fsys, err := FS(dir)
	if err != nil {
		t.Fatal(err)
	}
	data, err := fs.ReadFile(fsys, "index.html")
	if err != nil || string(data) != "dev" {
		t.Fatalf("dev fs: %q %v", data, err)
	}
}

func TestEmbeddedFSIsRooted(t *testing.T) {
	fsys, err := FS("")
	if err != nil {
		t.Fatal(err)
	}
	// After `make embed-shell` index.html exists; before, only .gitkeep is embedded.
	if _, err := fs.Stat(fsys, ".gitkeep"); err != nil {
		t.Fatalf("embedded fs not rooted at dist: %v", err)
	}
}
