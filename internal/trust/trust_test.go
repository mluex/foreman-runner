package trust

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readConfig(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	return m
}

func trusted(t *testing.T, cfg map[string]any, dir string) bool {
	t.Helper()
	projects, ok := cfg["projects"].(map[string]any)
	if !ok {
		return false
	}
	entry, ok := projects[dir].(map[string]any)
	if !ok {
		return false
	}
	v, _ := entry["hasTrustDialogAccepted"].(bool)
	return v
}

func TestSeedCreatesConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	dir := "/work/repo"

	changed, err := Seed(path, dir)
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true on first seed")
	}
	if !trusted(t, readConfig(t, path), dir) {
		t.Fatal("directory not marked trusted")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestSeedIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	dir := "/work/repo"

	if _, err := Seed(path, dir); err != nil {
		t.Fatalf("first Seed: %v", err)
	}
	changed, err := Seed(path, dir)
	if err != nil {
		t.Fatalf("second Seed: %v", err)
	}
	if changed {
		t.Fatal("expected changed=false when already trusted")
	}
}

func TestSeedPreservesExistingFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	existing := map[string]any{
		"userID": "abc123",
		"projects": map[string]any{
			"/other/repo": map[string]any{
				"hasTrustDialogAccepted": true,
				"customField":            "keep-me",
			},
		},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("seed existing: %v", err)
	}

	if _, err := Seed(path, "/work/repo"); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	cfg := readConfig(t, path)
	if cfg["userID"] != "abc123" {
		t.Errorf("top-level field lost: userID=%v", cfg["userID"])
	}
	if !trusted(t, cfg, "/other/repo") {
		t.Error("pre-existing trusted project was altered")
	}
	other := cfg["projects"].(map[string]any)["/other/repo"].(map[string]any)
	if other["customField"] != "keep-me" {
		t.Errorf("unknown project field lost: %v", other["customField"])
	}
	if !trusted(t, cfg, "/work/repo") {
		t.Error("new directory not marked trusted")
	}
}

func TestSeedFlipsExplicitFalse(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	dir := "/work/repo"
	existing := map[string]any{
		"projects": map[string]any{
			dir: map[string]any{"hasTrustDialogAccepted": false},
		},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("seed existing: %v", err)
	}

	changed, err := Seed(path, dir)
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true when flipping false to true")
	}
	if !trusted(t, readConfig(t, path), dir) {
		t.Fatal("directory not marked trusted")
	}
}
