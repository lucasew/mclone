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
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected config file to be created: %v", err)
	}
	if perm := info.Mode().Perm(); perm != configFileMode {
		t.Fatalf("created config mode = %04o, want %04o", perm, configFileMode)
	}
}

func TestSave_WritesOwnerOnlyMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mclone.conf")
	loader := &ConfigLoader{Location: path}

	if err := loader.Save(&Config{
		Remotes: map[string]RemoteConfig{
			"openai": {
				Type: "openai",
				Options: map[string]any{
					"api_key": "sk-secret",
				},
			},
		},
		Tools: map[string]ToolConfig{},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat after Save: %v", err)
	}
	if perm := info.Mode().Perm(); perm != configFileMode {
		t.Fatalf("saved config mode = %04o, want %04o", perm, configFileMode)
	}
}

func TestLoad_TightensLoosePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mclone.conf")
	// Pre-existing config may be 0644 from older mclone versions.
	if err := os.WriteFile(path, []byte("[remotes]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	loader := &ConfigLoader{Location: path}
	if _, err := loader.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != configFileMode {
		t.Fatalf("after Load mode = %04o, want %04o", perm, configFileMode)
	}
}

func TestSave_TightensExistingLooseFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mclone.conf")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	loader := &ConfigLoader{Location: path}
	if err := loader.Save(&Config{
		Remotes: map[string]RemoteConfig{},
		Tools:   map[string]ToolConfig{},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != configFileMode {
		t.Fatalf("after Save mode = %04o, want %04o", perm, configFileMode)
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
