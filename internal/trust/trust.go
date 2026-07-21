// Package trust pre-accepts Claude Code's per-directory workspace trust dialog.
//
// Claude Code shows a one-time "Is this a project you trust?" gate the first
// time it opens a directory interactively, and waits for input. That blocks an
// unattended runner. --dangerously-skip-permissions does not skip this gate; it
// only affects in-session permission prompts. The trust decision is stored in
// the user config (~/.claude.json) under projects.<dir>.hasTrustDialogAccepted,
// so the runner seeds that flag before launch.
package trust

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// DefaultConfigPath returns the Claude Code user config path (~/.claude.json).
func DefaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".claude.json"
	}
	return filepath.Join(home, ".claude.json")
}

// Seed marks dir as trusted in the Claude Code config at configPath, creating
// the file if it does not exist. It preserves every other field and writes
// atomically. It reports whether a change was made.
//
// Note: this is a read-modify-write on a shared file. A runner must run as its
// own OS user so it does not race a human's interactive Claude Code session on
// the same config.
func Seed(configPath, dir string) (changed bool, err error) {
	root := map[string]any{}
	data, err := os.ReadFile(configPath)
	switch {
	case err == nil:
		if len(data) > 0 {
			if err := json.Unmarshal(data, &root); err != nil {
				return false, fmt.Errorf("parse %s: %w", configPath, err)
			}
		}
	case os.IsNotExist(err):
		// start from an empty config
	default:
		return false, fmt.Errorf("read %s: %w", configPath, err)
	}

	projects, _ := root["projects"].(map[string]any)
	if projects == nil {
		projects = map[string]any{}
		root["projects"] = projects
	}
	entry, _ := projects[dir].(map[string]any)
	if entry == nil {
		entry = map[string]any{}
		projects[dir] = entry
	}

	if accepted, ok := entry["hasTrustDialogAccepted"].(bool); ok && accepted {
		return false, nil
	}
	entry["hasTrustDialogAccepted"] = true
	if _, ok := entry["projectOnboardingSeenCount"]; !ok {
		entry["projectOnboardingSeenCount"] = 1
	}

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return false, fmt.Errorf("encode config: %w", err)
	}
	if err := writeAtomic(configPath, out); err != nil {
		return false, err
	}
	return true, nil
}

// writeAtomic writes data to path via a temp file in the same directory and a
// rename, so a crash cannot leave a truncated config. Mode is 0600.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".claude-config-*.tmp")
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
