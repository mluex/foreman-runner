package taskstate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoadRoundTripsAState(t *testing.T) {
	dir := t.TempDir()
	want := State{
		TaskID:      "task-1",
		Session:     "foreman-task-task-1",
		LogFile:     filepath.Join(dir, "task-task-1.log"),
		ExitFile:    filepath.Join(dir, "task-task-1.exit"),
		EncryptLogs: true,
		Seq:         7,
		Offset:      4096,
	}

	if err := Save(dir, want); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := Load(dir, want.TaskID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got == nil {
		t.Fatal("load returned no state")
	}
	if *got != want {
		t.Fatalf("loaded state = %+v, want %+v", *got, want)
	}
}

func TestLoadReportsNoStateForAnUnknownTask(t *testing.T) {
	got, err := Load(t.TempDir(), "task-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got != nil {
		t.Fatalf("load returned %+v, want no state", *got)
	}
}

func TestListReturnsEveryStoredStateAndIgnoresOtherFiles(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"task-1", "task-2"} {
		if err := Save(dir, State{TaskID: id, Seq: 1}); err != nil {
			t.Fatalf("save %s: %v", id, err)
		}
	}
	// the log and exit files of a task live in the same directory
	if err := os.WriteFile(filepath.Join(dir, "task-task-1.log"), []byte("output"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "task-task-3.state"), []byte("not json"), 0o600); err != nil {
		t.Fatalf("write damaged state: %v", err)
	}

	states := List(dir)
	if len(states) != 2 {
		t.Fatalf("listed %d states, want 2: %+v", len(states), states)
	}
}

func TestRemoveDeletesTheStateFile(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, State{TaskID: "task-1"}); err != nil {
		t.Fatalf("save: %v", err)
	}

	Remove(dir, "task-1")

	if _, err := os.Stat(Path(dir, "task-1")); !os.IsNotExist(err) {
		t.Fatalf("state file still present: %v", err)
	}
}
