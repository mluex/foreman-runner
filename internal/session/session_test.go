package session

import (
	"strings"
	"testing"
)

func TestAgentCommandPassesPromptAsTrailingArg(t *testing.T) {
	argv := Spec{
		ClaudeBin:      "claude",
		PermissionMode: "auto",
		Model:          "opus",
		RemoteControl:  true,
		SessionName:    "my task",
		Prompt:         "fix the login bug",
	}.agentCommand()

	last := argv[len(argv)-1]
	if last != "fix the login bug" {
		t.Fatalf("prompt must be the trailing arg, got %q (argv: %v)", last, argv)
	}

	joined := strings.Join(argv, " ")
	if strings.Contains(joined, "/remote-control") {
		t.Errorf("prompt must not be prefixed with the /remote-control slash command: %v", argv)
	}

	// --remote-control must be immediately followed by an explicit name so it
	// cannot consume the prompt as its optional [name] argument
	idx := indexOf(argv, "--remote-control")
	if idx < 0 {
		t.Fatalf("expected --remote-control flag, got %v", argv)
	}
	if idx+1 >= len(argv) || argv[idx+1] != "my task" {
		t.Errorf("--remote-control must be followed by the explicit session name, got %v", argv)
	}
	if argv[idx+1] == "fix the login bug" {
		t.Errorf("--remote-control must not consume the prompt")
	}
}

func TestAgentCommandRemoteControlNameFallsBackToSessionName(t *testing.T) {
	argv := Spec{
		ClaudeBin:     "claude",
		Name:          "foreman-task-123",
		RemoteControl: true,
		Prompt:        "do the thing",
	}.agentCommand()

	idx := indexOf(argv, "--remote-control")
	if idx < 0 || idx+1 >= len(argv) || argv[idx+1] != "foreman-task-123" {
		t.Errorf("expected --remote-control to fall back to Name, got %v", argv)
	}
}

func TestAgentCommandWithoutRemoteControl(t *testing.T) {
	argv := Spec{ClaudeBin: "claude", Prompt: "hello"}.agentCommand()
	if indexOf(argv, "--remote-control") >= 0 {
		t.Errorf("did not expect --remote-control, got %v", argv)
	}
	if argv[len(argv)-1] != "hello" {
		t.Errorf("prompt must be the trailing arg, got %v", argv)
	}
}

func indexOf(items []string, target string) int {
	for i, item := range items {
		if item == target {
			return i
		}
	}

	return -1
}

func TestAgentCommandResumesInsteadOfSubmittingThePrompt(t *testing.T) {
	argv := Spec{
		ClaudeBin:       "claude",
		PermissionMode:  "auto",
		RemoteControl:   true,
		SessionName:     "my task",
		Prompt:          "fix the login bug",
		ResumeSessionID: "2a3310d3-a725-4ef3-bbf4-b9ff5616bf3d",
	}.agentCommand()

	if indexOf(argv, "fix the login bug") >= 0 {
		t.Errorf("a resume must not submit the original prompt again: %v", argv)
	}
	idx := indexOf(argv, "--resume")
	if idx < 0 || idx+1 >= len(argv) || argv[idx+1] != "2a3310d3-a725-4ef3-bbf4-b9ff5616bf3d" {
		t.Errorf("expected --resume followed by the session id, got %v", argv)
	}
	rc := indexOf(argv, "--remote-control")
	if rc < 0 || argv[rc+1] != "my task" {
		t.Errorf("a resumed session keeps Remote Control with its explicit name, got %v", argv)
	}
}
