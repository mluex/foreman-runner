package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildPlanLinux(t *testing.T) {
	p, err := BuildPlan("linux", "/home/x", "/usr/bin:/bin")
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
	if !strings.Contains(p.UnitContent, "Environment=PATH=/home/x/.local/bin:") {
		t.Errorf("unit missing PATH environment:\n%s", p.UnitContent)
	}
	if !strings.Contains(strings.Join(p.EnableCommands, "\n"), "systemctl --user enable --now foreman-runner") {
		t.Errorf("enable commands = %v", p.EnableCommands)
	}
}

func TestBuildPlanDarwin(t *testing.T) {
	p, err := BuildPlan("darwin", "/Users/x", "/usr/bin:/bin")
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if p.UnitPath != "/Users/x/Library/LaunchAgents/net.mluex.foreman-runner.plist" {
		t.Errorf("UnitPath = %q", p.UnitPath)
	}
	if !strings.Contains(p.UnitContent, "/Users/x/.local/bin/foreman-runner") {
		t.Errorf("plist missing binary path:\n%s", p.UnitContent)
	}
	if !strings.Contains(p.UnitContent, "<key>PATH</key>") || !strings.Contains(p.UnitContent, "/Users/x/.local/bin") {
		t.Errorf("plist missing PATH environment:\n%s", p.UnitContent)
	}
	if len(p.EnableCommands) != 1 || !strings.HasPrefix(p.EnableCommands[0], "launchctl load ") {
		t.Errorf("enable commands = %v", p.EnableCommands)
	}
}

func TestBuildPlanUnsupported(t *testing.T) {
	if _, err := BuildPlan("windows", "/home/x", ""); err == nil {
		t.Fatal("expected an error for an unsupported OS")
	}
}

func TestServicePATHLeadsWithUserBinAndDedupes(t *testing.T) {
	got := servicePATH("/home/x", "/home/x/.local/bin:/usr/bin:/opt/tools/bin")
	want := "/home/x/.local/bin:/home/x/bin:/usr/bin:/opt/tools/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/sbin:/bin"
	if got != want {
		t.Errorf("servicePATH:\n got  %q\n want %q", got, want)
	}
}

func TestServicePATHFallsBackWhenCurrentPathEmpty(t *testing.T) {
	got := servicePATH("/home/x", "")
	for _, dir := range []string{"/home/x/.local/bin", "/usr/local/bin", "/usr/bin", "/bin"} {
		if !strings.Contains(got, dir) {
			t.Errorf("servicePATH(%q) missing %q: %q", "", dir, got)
		}
	}
}

func TestApplyWritesBinaryAndUnit(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(t.TempDir(), "foreman-runner")
	if err := os.WriteFile(src, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	p, err := BuildPlan("linux", home, "/usr/bin:/bin")
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
