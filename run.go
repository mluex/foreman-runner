package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mluex/foreman-runner/internal/session"
	"github.com/mluex/foreman-runner/internal/trust"
)

// stringSlice collects a repeatable string flag.
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, " ") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// cmdRun is the proof-of-concept launcher: spawn a tmux session in a target
// directory, start Claude Code there, and submit a prompt.
func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)

	var extraArgs stringSlice
	fs.Var(&extraArgs, "claude-arg", "extra arg passed to claude verbatim (repeatable), e.g. --claude-arg=--dangerously-skip-permissions")
	var (
		dir           = fs.String("dir", "", "working directory the agent runs in (required)")
		prompt        = fs.String("prompt", "", "prompt submitted to the agent (required)")
		model         = fs.String("model", "", "model alias passed to claude --model (empty = claude default)")
		permMode      = fs.String("permission-mode", "auto", "claude --permission-mode value")
		remoteControl = fs.Bool("remote-control", true, "prefix the prompt with /remote-control")
		name          = fs.String("session", "", "tmux session name (empty = generated)")
		taskID        = fs.String("task-id", "", "task id exposed as FOREMAN_TASK_ID (empty = generated)")
		clickUpID     = fs.String("clickup-id", "", "ClickUp id exposed as FOREMAN_CLICKUP_ID")
		skipPerms     = fs.Bool("skip-permissions", false, "launch claude with --dangerously-skip-permissions (adds its own one-time acceptance gate; auto mode is preferred for unattended runs)")
		trustDir      = fs.Bool("trust", true, "pre-accept the workspace trust dialog for --dir in the claude config")
		claudeConfig  = fs.String("claude-config", trust.DefaultConfigPath(), "path to the claude user config for trust seeding")
		claudeBin     = fs.String("claude-bin", "claude", "agent binary name or path")
		logDir        = fs.String("log-dir", defaultLogDir(), "directory for the captured session log")
		attach        = fs.Bool("attach", false, "attach to the tmux session after launch")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *dir == "" {
		return fmt.Errorf("--dir is required")
	}
	if *prompt == "" {
		return fmt.Errorf("--prompt is required")
	}

	absDir, err := filepath.Abs(*dir)
	if err != nil {
		return fmt.Errorf("resolve --dir: %w", err)
	}

	id := *taskID
	if id == "" {
		id = genID()
	}
	sessName := *name
	if sessName == "" {
		sessName = "foreman-task-" + id
	}

	finalPrompt := *prompt
	if *remoteControl && !strings.HasPrefix(strings.TrimSpace(finalPrompt), "/remote-control") {
		finalPrompt = "/remote-control " + finalPrompt
	}

	// --permission-mode auto handles in-session permissions unattended, so
	// --dangerously-skip-permissions is off by default: it does not skip the
	// trust gate and adds its own one-time "Bypass Permissions" acceptance
	// dialog that stalls the session. Prepend so an explicit --claude-arg wins.
	if *skipPerms {
		extraArgs = append([]string{"--dangerously-skip-permissions"}, extraArgs...)
	}

	// The workspace trust dialog is a separate gate that --dangerously-skip-
	// permissions does not bypass; pre-accept it for the target directory so an
	// interactive session does not stall waiting for input.
	if *trustDir {
		changed, err := trust.Seed(*claudeConfig, absDir)
		if err != nil {
			return fmt.Errorf("seed workspace trust: %w", err)
		}
		if changed {
			fmt.Printf("trust     seeded hasTrustDialogAccepted for %s\n", absDir)
		}
	}

	res, err := session.Launch(session.Spec{
		Name:           sessName,
		TaskID:         id,
		ClickUpID:      *clickUpID,
		Dir:            absDir,
		Prompt:         finalPrompt,
		Model:          *model,
		PermissionMode: *permMode,
		ClaudeBin:      *claudeBin,
		ExtraArgs:      extraArgs,
		LogDir:         *logDir,
	})
	if err != nil {
		return err
	}

	fmt.Printf("session   %s\n", res.Name)
	fmt.Printf("task id   %s\n", id)
	fmt.Printf("dir       %s\n", absDir)
	fmt.Printf("log       %s\n", res.LogFile)
	fmt.Printf("command   %s\n", strings.Join(res.Command, " "))
	fmt.Printf("\nattach:   tmux attach -t %s\n", res.Name)
	fmt.Printf("tail:     tail -f %s\n", res.LogFile)
	fmt.Printf("kill:     tmux kill-session -t %s\n", res.Name)

	if *attach {
		return attachTo(res.Name)
	}
	return nil
}

// attachTo replaces the current process with an interactive tmux attach.
func attachTo(name string) error {
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		return err
	}
	cmd := exec.Command(tmuxBin, "attach", "-t", name)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// defaultLogDir follows the XDG state convention, matching the briefing.
func defaultLogDir() string {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "foreman")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "foreman")
	}
	return filepath.Join(home, ".local", "state", "foreman")
}

// genID returns a short random hex id for a PoC task.
func genID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "poc"
	}
	return hex.EncodeToString(b)
}
