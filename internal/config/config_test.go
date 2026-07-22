package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "runner.json")

	in := &Config{
		RunnerID:      "0198-runner",
		RunnerPrivKey: "cHJpdmF0ZQ==",
		APIToken:      "token-123",
		UserPubKey:    "cHVibGlj",
		ServerURL:     "https://foreman.example.com",
		OS:            "linux",
		Arch:          "arm64",
	}

	if err := Save(path, in); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if !Exists(path) {
		t.Fatal("Exists returned false after Save")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config mode = %o, want 600", perm)
	}

	out, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if *out != *in {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", out, in)
	}
}
