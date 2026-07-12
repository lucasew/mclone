package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestGetLocation_CreatesMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mclone.conf")

	loader := &ConfigLoader{Location: path}
	got, err := loader.GetLocation()
	if err != nil {
		t.Fatalf("GetLocation: %v", err)
	}
	if got != path {
		t.Fatalf("GetLocation path = %q, want %q", got, path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected config file to be created: %v", err)
	}
}

func TestGetLocation_ExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mclone.conf")
	if err := os.WriteFile(path, []byte("remotes = {}"), 0o644); err != nil {
		t.Fatal(err)
	}

	loader := &ConfigLoader{Location: path}
	got, err := loader.GetLocation()
	if err != nil {
		t.Fatalf("GetLocation: %v", err)
	}
	if got != path {
		t.Fatalf("GetLocation path = %q, want %q", got, path)
	}
}

func TestGetLocation_SurfacesNonNotExistStatError(t *testing.T) {
	dir := t.TempDir()
	// Stat fails with ENOTDIR when a path component is a file, not a directory.
	blocker := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(blocker, "mclone.conf")

	loader := &ConfigLoader{Location: path}
	_, err := loader.GetLocation()
	if err == nil {
		t.Fatal("GetLocation: expected error for non-NotExist Stat failure")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("GetLocation: unexpected NotExist error: %v", err)
	}
}
