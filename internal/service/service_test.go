package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildPlanLinux(t *testing.T) {
	p, err := BuildPlan("linux", "/home/x")
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if p.BinaryDest != "/home/x/.local/bin/foreman-runner" {
		t.Errorf("BinaryDest = %q", p.BinaryDest)
	}
	if p.UnitPath != "/home/x/.config/systemd/user/foreman-runner.service" {
		t.Errorf("UnitPath = %q", p.UnitPath)
	}
	if !strings.Contains(p.UnitContent, "ExecStart=/home/x/.local/bin/foreman-runner run") {
		t.Errorf("unit missing ExecStart:\n%s", p.UnitContent)
	}
	if !strings.Contains(strings.Join(p.EnableCommands, "\n"), "systemctl --user enable --now foreman-runner") {
		t.Errorf("enable commands = %v", p.EnableCommands)
	}
}

func TestBuildPlanDarwin(t *testing.T) {
	p, err := BuildPlan("darwin", "/Users/x")
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if p.UnitPath != "/Users/x/Library/LaunchAgents/net.mluex.foreman-runner.plist" {
		t.Errorf("UnitPath = %q", p.UnitPath)
	}
	if !strings.Contains(p.UnitContent, "/Users/x/.local/bin/foreman-runner") {
		t.Errorf("plist missing binary path:\n%s", p.UnitContent)
	}
	if len(p.EnableCommands) != 1 || !strings.HasPrefix(p.EnableCommands[0], "launchctl load ") {
		t.Errorf("enable commands = %v", p.EnableCommands)
	}
}

func TestBuildPlanUnsupported(t *testing.T) {
	if _, err := BuildPlan("windows", "/home/x"); err == nil {
		t.Fatal("expected an error for an unsupported OS")
	}
}

func TestApplyWritesBinaryAndUnit(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(t.TempDir(), "foreman-runner")
	if err := os.WriteFile(src, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	p, err := BuildPlan("linux", home)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(src); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if data, err := os.ReadFile(p.BinaryDest); err != nil || string(data) != "binary" {
		t.Errorf("binary not installed: %v", err)
	}
	unit, err := os.ReadFile(p.UnitPath)
	if err != nil || !strings.Contains(string(unit), "ExecStart=") {
		t.Errorf("unit not written: %v", err)
	}
}
