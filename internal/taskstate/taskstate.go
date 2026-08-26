// Package taskstate persists what the runner needs to pick a task's session up
// again after a restart: where its log is, how far it has been streamed, and
// whether its chunks are sealed. The file lives next to the task log and is
// removed once the task is reported finished.
package taskstate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	prefix    = "task-"
	extension = ".state"
)

// State is one supervised task's on-disk bookkeeping.
type State struct {
	TaskID      string `json:"task_id"`
	Session     string `json:"session"`
	LogFile     string `json:"log_file"`
	ExitFile    string `json:"exit_file"`
	EncryptLogs bool   `json:"encrypt_logs"`
	Seq         int    `json:"seq"`
	Offset      int64  `json:"offset"`
}

// Path returns the state file of a task inside dir.
func Path(dir, taskID string) string {
	return filepath.Join(dir, prefix+taskID+extension)
}

// Save writes s atomically, creating dir when needed.
func Save(dir string, s State) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("encode task state: %w", err)
	}

	tmp, err := os.CreateTemp(dir, "."+prefix+"*.tmp")
	if err != nil {
		return fmt.Errorf("create temp task state: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp task state: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp task state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp task state: %w", err)
	}
	if err := os.Rename(tmpName, Path(dir, s.TaskID)); err != nil {
		return fmt.Errorf("replace task state: %w", err)
	}

	return nil
}

// Load reads the state of one task. A missing file yields (nil, nil): the task
// is simply not one this runner has bookkeeping for.
func Load(dir, taskID string) (*State, error) {
	data, err := os.ReadFile(Path(dir, taskID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read task state: %w", err)
	}

	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse task state %s: %w", Path(dir, taskID), err)
	}

	return &s, nil
}

// Remove deletes a task's state file. A missing file is not an error.
func Remove(dir, taskID string) {
	_ = os.Remove(Path(dir, taskID))
}

// List returns every task state stored in dir, skipping files that cannot be
// read or parsed - a damaged file must not keep the runner from starting.
func List(dir string) []State {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var states []State
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, extension) {
			continue
		}
		taskID := strings.TrimSuffix(strings.TrimPrefix(name, prefix), extension)
		state, err := Load(dir, taskID)
		if err != nil || state == nil {
			continue
		}
		states = append(states, *state)
	}

	return states
}
