// Package service installs the runner as a background service that starts on
// boot: a systemd user unit on Linux, a LaunchAgent on macOS. It only writes
// the files; enabling them is left to the operator (the commands are printed).
package service

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// DarwinLabel is the launchd label of the runner LaunchAgent.
const DarwinLabel = "net.mluex.foreman-runner"

// Plan describes what installing the service will do on this platform.
type Plan struct {
	// BinaryDest is where the runner binary is installed.
	BinaryDest string
	// UnitPath is the service definition file to write.
	UnitPath string
	// UnitContent is the contents of that file.
	UnitContent string
	// EnableCommands are printed for the operator to run.
	EnableCommands []string
}

// BuildPlan computes the install plan for goos with the given home directory.
// currentPath is the operator's PATH at install time; it is baked into the
// service definition so the background service resolves agent binaries (such
// as "claude") the same way the operator's own shell does. Managed services
// otherwise start with a bare PATH that omits per-user bin directories, which
// makes agent launches fail with "executable file not found in $PATH".
func BuildPlan(goos, home, currentPath string) (Plan, error) {
	binDest := filepath.Join(home, ".local", "bin", "foreman-runner")
	servicePath := servicePATH(home, currentPath)

	switch goos {
	case "linux":
		unit := filepath.Join(home, ".config", "systemd", "user", "foreman-runner.service")

		return Plan{
			BinaryDest:  binDest,
			UnitPath:    unit,
			UnitContent: linuxUnit(binDest, servicePath),
			EnableCommands: []string{
				"systemctl --user daemon-reload",
				"systemctl --user enable --now foreman-runner",
				"loginctl enable-linger \"$(whoami)\"   # optional: keep it running without an active login",
			},
		}, nil
	case "darwin":
		plist := filepath.Join(home, "Library", "LaunchAgents", "net.mluex.foreman-runner.plist")

		return Plan{
			BinaryDest:     binDest,
			UnitPath:       plist,
			UnitContent:    darwinPlist(binDest, filepath.Join(home, ".local", "state", "foreman", "runner.log"), servicePath),
			EnableCommands: []string{fmt.Sprintf("launchctl load %s", plist)},
		}, nil
	default:
		return Plan{}, fmt.Errorf("service install is not supported on %s; run \"foreman-runner run\" manually", goos)
	}
}

// servicePATH builds the PATH the background service should run with. It leads
// with the user's own bin directories, then appends the operator's current
// PATH, then a set of standard system directories as a fallback for the case
// where install ran with an empty environment. Duplicates are dropped while the
// first occurrence order is kept.
func servicePATH(home, currentPath string) string {
	var dirs []string
	seen := make(map[string]bool)
	add := func(dir string) {
		if dir == "" || seen[dir] {
			return
		}
		seen[dir] = true
		dirs = append(dirs, dir)
	}

	add(filepath.Join(home, ".local", "bin"))
	add(filepath.Join(home, "bin"))
	for _, dir := range filepath.SplitList(currentPath) {
		add(dir)
	}
	for _, dir := range []string{"/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin"} {
		add(dir)
	}

	return strings.Join(dirs, ":")
}

// Apply installs the binary (copied from currentExec) and writes the unit file.
// It is a no-op copy when currentExec already is the destination.
func (p Plan) Apply(currentExec string) error {
	if err := os.MkdirAll(filepath.Dir(p.BinaryDest), 0o755); err != nil {
		return fmt.Errorf("create bin dir: %w", err)
	}
	if currentExec != p.BinaryDest {
		if err := copyFile(currentExec, p.BinaryDest, 0o755); err != nil {
			return fmt.Errorf("install binary: %w", err)
		}
	}

	if err := os.MkdirAll(filepath.Dir(p.UnitPath), 0o755); err != nil {
		return fmt.Errorf("create unit dir: %w", err)
	}
	if err := os.WriteFile(p.UnitPath, []byte(p.UnitContent), 0o644); err != nil {
		return fmt.Errorf("write unit file: %w", err)
	}

	return nil
}

func linuxUnit(binPath, pathEnv string) string {
	return fmt.Sprintf(`[Unit]
Description=foreman runner
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
Environment=PATH=%s
ExecStart=%s run
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`, pathEnv, binPath)
}

func darwinPlist(binPath, logPath, pathEnv string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>net.mluex.foreman-runner</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>run</string>
	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>%s</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, binPath, pathEnv, logPath, logPath)
}

// DefaultHome returns the current user's home directory.
func DefaultHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	return home
}

// CurrentGOOS returns the OS the runner was built for.
func CurrentGOOS() string {
	return runtime.GOOS
}

// Restart restarts the installed background service so it picks up a freshly
// updated binary: systemctl on Linux, launchctl on macOS. It reports an error
// (rather than acting) on platforms without a managed service.
func Restart(goos string) error {
	switch goos {
	case "linux":
		return runCommand("systemctl", "--user", "restart", "foreman-runner")
	case "darwin":
		return runCommand("launchctl", "kickstart", "-k", fmt.Sprintf("gui/%d/%s", os.Getuid(), DarwinLabel))
	default:
		return fmt.Errorf("automatic restart is not supported on %s", goos)
	}
}

func runCommand(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}

	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)

		return err
	}
	if err := out.Chmod(mode); err != nil {
		out.Close()
		os.Remove(tmp)

		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)

		return err
	}

	return os.Rename(tmp, dst)
}
