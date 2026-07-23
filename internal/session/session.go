// Package session launches a coding-agent run inside a detached tmux session
// and wires up log capture. It is the core of the foreman runner: the server
// hands it a task, this package turns it into a live, observable process.
package session

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Spec describes a single agent run to launch.
type Spec struct {
	// Name is the tmux session name, e.g. "foreman-task-<uuid>".
	Name string
	// TaskID is exposed to the agent as FOREMAN_TASK_ID.
	TaskID string
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
	// RemoteControl enables Claude Code's Remote Control (--remote-control) so
	// the live session is controllable from Claude Web.
	RemoteControl bool
	// SessionName is the display name for the Remote Control session. It is
	// passed as the explicit name of --remote-control so the flag cannot swallow
	// the prompt argument that follows it. Falls back to Name when empty.
	SessionName string
	// ExtraArgs are passed to claude verbatim, before the prompt (e.g.
	// --dangerously-skip-permissions to bypass the workspace trust dialog).
	ExtraArgs []string
	// LogDir is where the piped session log is written.
	LogDir string
	// ExitCodeFile, when set, makes the pane run the agent non-exec and write
	// the agent's exit code to this path when it finishes. Used by the task
	// dispatcher to report completion; leave empty for an interactive PoC run.
	ExitCodeFile string
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
	if out, err := exec.Command(tmuxBin, "send-keys", "-t", s.Name, "-l", s.commandLine(agentArgs)).CombinedOutput(); err != nil {
		_ = exec.Command(tmuxBin, "kill-session", "-t", s.Name).Run()
		return nil, fmt.Errorf("tmux send-keys failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command(tmuxBin, "send-keys", "-t", s.Name, "Enter").CombinedOutput(); err != nil {
		_ = exec.Command(tmuxBin, "kill-session", "-t", s.Name).Run()
		return nil, fmt.Errorf("tmux send-keys Enter failed: %w: %s", err, strings.TrimSpace(string(out)))
	}

	// In interactive mode a session that vanished immediately means the agent
	// failed to start. In task mode (ExitCodeFile set) the agent may legitimately
	// finish before this check, so completion is detected via WaitExit instead.
	if s.ExitCodeFile == "" && !hasSession(tmuxBin, s.Name) {
		return nil, fmt.Errorf("session %q exited immediately; check that the agent is authenticated", s.Name)
	}

	return &Result{Name: s.Name, LogFile: logFile, Command: agentArgs}, nil
}

// agentCommand builds the argv for the coding agent. It starts an interactive
// session (no -p) so the run stays alive, enabling Remote Control via the
// native --remote-control flag, and passes the task prompt as the prompt
// argument so Claude acts on it as the first turn.
func (s Spec) agentCommand() []string {
	args := []string{s.ClaudeBin}
	if s.PermissionMode != "" {
		args = append(args, "--permission-mode", s.PermissionMode)
	}
	if s.Model != "" {
		args = append(args, "--model", s.Model)
	}
	if s.RemoteControl {
		// --remote-control takes an optional [name]; always pass an explicit
		// name so it cannot consume the prompt argument that follows it.
		name := s.SessionName
		if name == "" {
			name = s.Name
		}
		args = append(args, "--remote-control", name)
	}
	args = append(args, s.ExtraArgs...)
	args = append(args, s.Prompt)
	return args
}

// commandLine renders argv as a single shell line, quoting every field so the
// prompt survives the pane shell verbatim. In task mode it runs non-exec and
// appends the exit-code capture; otherwise it execs the agent so the process
// tree ends at the agent.
func (s Spec) commandLine(argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = shellQuote(a)
	}
	joined := strings.Join(quoted, " ")

	if s.ExitCodeFile != "" {
		// capture the agent's exit code, record it, then exit the pane shell
		// with the same code so the tmux session actually ends
		return fmt.Sprintf("%s; _fr_ec=$?; echo $_fr_ec > %s; exit $_fr_ec", joined, shellQuote(s.ExitCodeFile))
	}
	return "exec " + joined
}

// WaitExit blocks until the named tmux session ends, then reads the exit code
// the pane wrote to exitFile.
func WaitExit(name, exitFile string, poll time.Duration) (int, error) {
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		return -1, err
	}

	for hasSession(tmuxBin, name) {
		time.Sleep(poll)
	}

	data, err := os.ReadFile(exitFile)
	if err != nil {
		return -1, fmt.Errorf("read exit code: %w", err)
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return -1, fmt.Errorf("parse exit code %q: %w", strings.TrimSpace(string(data)), err)
	}
	return code, nil
}

// Cancel gracefully shuts a running session down. It sends the agent two
// Ctrl-C keystrokes (Claude Code exits on a double interrupt) followed by
// /exit, waits up to grace for the session to end on its own, and hard-kills
// the tmux session as a last resort. It is a no-op if the session is already
// gone.
func Cancel(name string, grace time.Duration) error {
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		return err
	}
	if !hasSession(tmuxBin, name) {
		return nil
	}

	_ = exec.Command(tmuxBin, "send-keys", "-t", name, "C-c").Run()
	time.Sleep(300 * time.Millisecond)
	_ = exec.Command(tmuxBin, "send-keys", "-t", name, "C-c").Run()
	time.Sleep(300 * time.Millisecond)
	_ = exec.Command(tmuxBin, "send-keys", "-t", name, "-l", "/exit").Run()
	_ = exec.Command(tmuxBin, "send-keys", "-t", name, "Enter").Run()

	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if !hasSession(tmuxBin, name) {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}

	if !hasSession(tmuxBin, name) {
		return nil
	}
	return exec.Command(tmuxBin, "kill-session", "-t", name).Run()
}

func hasSession(tmuxBin, name string) bool {
	return exec.Command(tmuxBin, "has-session", "-t", name).Run() == nil
}

// shellQuote wraps s in single quotes, escaping any embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
