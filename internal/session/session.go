// Package session launches a coding-agent run inside a detached tmux session
// and wires up log capture. It is the core of the foreman runner: the server
// hands it a task, this package turns it into a live, observable process.
package session

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Spec describes a single agent run to launch.
type Spec struct {
	// Name is the tmux session name, e.g. "foreman-task-<uuid>".
	Name string
	// TaskID is exposed to the agent as FOREMAN_TASK_ID.
	TaskID string
	// ClickUpID is exposed as FOREMAN_CLICKUP_ID when set.
	ClickUpID string
	// Dir is the working directory the agent runs in. Must exist.
	Dir string
	// Prompt is the full text submitted to the agent on start.
	Prompt string
	// Model is passed to claude via --model. Empty means claude's default.
	Model string
	// PermissionMode maps to claude --permission-mode (e.g. "auto").
	PermissionMode string
	// ClaudeBin is the agent binary name or path.
	ClaudeBin string
	// ExtraArgs are passed to claude verbatim, before the prompt (e.g.
	// --dangerously-skip-permissions to bypass the workspace trust dialog).
	ExtraArgs []string
	// LogDir is where the piped session log is written.
	LogDir string
}

// Result reports where a launched session lives.
type Result struct {
	Name    string
	LogFile string
	Command []string
}

// Launch spawns the tmux session, starts the agent, and begins log capture.
// It fails loudly if tmux or the agent binary are missing, or if the working
// directory does not exist.
func Launch(s Spec) (*Result, error) {
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		return nil, fmt.Errorf("tmux not found on PATH: %w", err)
	}
	if _, err := exec.LookPath(s.ClaudeBin); err != nil {
		return nil, fmt.Errorf("agent binary %q not found on PATH: %w", s.ClaudeBin, err)
	}

	info, err := os.Stat(s.Dir)
	if err != nil {
		return nil, fmt.Errorf("working directory %q: %w", s.Dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("working directory %q is not a directory", s.Dir)
	}

	if err := os.MkdirAll(s.LogDir, 0o700); err != nil {
		return nil, fmt.Errorf("create log dir %q: %w", s.LogDir, err)
	}
	logFile := filepath.Join(s.LogDir, "task-"+s.TaskID+".log")

	// Refuse to reuse a live session name; it would silently attach to the
	// wrong run.
	if hasSession(tmuxBin, s.Name) {
		return nil, fmt.Errorf("tmux session %q already exists", s.Name)
	}

	// Start an idle shell first so log capture can be armed before the agent
	// produces any output. new-session with an inline command races pipe-pane
	// and drops the first lines. -e sets session env inherited by the pane.
	newArgs := []string{
		"new-session", "-d",
		"-s", s.Name,
		"-c", s.Dir,
		"-e", "FOREMAN_TASK_ID=" + s.TaskID,
	}
	if s.ClickUpID != "" {
		newArgs = append(newArgs, "-e", "FOREMAN_CLICKUP_ID="+s.ClickUpID)
	}
	if out, err := exec.Command(tmuxBin, newArgs...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("tmux new-session failed: %w: %s", err, strings.TrimSpace(string(out)))
	}

	// pipe-pane runs its command through /bin/sh, so quote the log path.
	pipeCmd := fmt.Sprintf("cat >> %s", shellQuote(logFile))
	if out, err := exec.Command(tmuxBin, "pipe-pane", "-t", s.Name, "-o", pipeCmd).CombinedOutput(); err != nil {
		_ = exec.Command(tmuxBin, "kill-session", "-t", s.Name).Run()
		return nil, fmt.Errorf("tmux pipe-pane failed: %w: %s", err, strings.TrimSpace(string(out)))
	}

	// Now hand the agent command to the pane's shell. exec replaces the shell
	// so the process tree ends at the agent, and the shell's line editor holds
	// the keystrokes even if it is not fully ready yet.
	agentArgs := s.agentCommand()
	if out, err := exec.Command(tmuxBin, "send-keys", "-t", s.Name, "-l", shellCommandLine(agentArgs)).CombinedOutput(); err != nil {
		_ = exec.Command(tmuxBin, "kill-session", "-t", s.Name).Run()
		return nil, fmt.Errorf("tmux send-keys failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command(tmuxBin, "send-keys", "-t", s.Name, "Enter").CombinedOutput(); err != nil {
		_ = exec.Command(tmuxBin, "kill-session", "-t", s.Name).Run()
		return nil, fmt.Errorf("tmux send-keys Enter failed: %w: %s", err, strings.TrimSpace(string(out)))
	}

	if !hasSession(tmuxBin, s.Name) {
		return nil, fmt.Errorf("session %q exited immediately; check that the agent is authenticated", s.Name)
	}

	return &Result{Name: s.Name, LogFile: logFile, Command: agentArgs}, nil
}

// agentCommand builds the argv for the coding agent. It starts an interactive
// session (no -p) so the run stays alive and controllable via /remote-control.
func (s Spec) agentCommand() []string {
	args := []string{s.ClaudeBin}
	if s.PermissionMode != "" {
		args = append(args, "--permission-mode", s.PermissionMode)
	}
	if s.Model != "" {
		args = append(args, "--model", s.Model)
	}
	args = append(args, s.ExtraArgs...)
	args = append(args, s.Prompt)
	return args
}

// shellCommandLine renders argv as a single shell line that execs the agent,
// quoting every field so the prompt survives the pane shell verbatim.
func shellCommandLine(argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = shellQuote(a)
	}
	return "exec " + strings.Join(quoted, " ")
}

func hasSession(tmuxBin, name string) bool {
	return exec.Command(tmuxBin, "has-session", "-t", name).Run() == nil
}

// shellQuote wraps s in single quotes, escaping any embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
