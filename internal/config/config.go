// Package config reads and writes the runner's on-disk credentials.
//
// The file lives at ~/.config/foreman/runner.json (mode 0600) and holds
// everything the runner needs to talk to the server: its identity, its private
// signing key, the API bearer token, and the owner's public key used to verify
// task signatures.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config is the persisted runner state.
type Config struct {
	RunnerID      string `json:"runner_id"`
	RunnerPrivKey string `json:"runner_privkey"` // base64, 64-byte Ed25519 private key
	APIToken      string `json:"api_token"`
	UserPubKey    string `json:"user_pubkey"` // base64, 32-byte Ed25519 public key
	ServerURL     string `json:"server_url"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
}

// DefaultPath returns ~/.config/foreman/runner.json, honoring XDG_CONFIG_HOME.
func DefaultPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "foreman", "runner.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "foreman", "runner.json")
	}
	return filepath.Join(home, ".config", "foreman", "runner.json")
}

// Exists reports whether a config file is present at path.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Load reads and parses the config at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	return &c, nil
}

// Save writes c to path atomically with mode 0600, creating parent dirs (0700).
func Save(path string, c *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".runner-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}
