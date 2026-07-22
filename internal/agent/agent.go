// Package agent detects the coding agents available on this machine.
package agent

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/mluex/foreman-runner/internal/api"
)

var versionRe = regexp.MustCompile(`\d+\.\d+\.\d+`)

// Detect returns the agents present on the host. For v1 this is Claude Code:
// if the "claude" binary is on PATH, it is reported with its version. An entry
// is omitted entirely when the binary is absent.
func Detect() []api.Agent {
	path, err := exec.LookPath("claude")
	if err != nil {
		return nil
	}

	version := claudeVersion(path)

	return []api.Agent{
		{
			Name:    "claude-code",
			Version: version,
			Path:    path,
			// best-effort: a version response implies a working binary. A real
			// auth probe is not yet available, so treat detected as usable.
			AuthOK: version != "",
		},
	}
}

func claudeVersion(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return ""
	}

	if v := versionRe.FindString(string(out)); v != "" {
		return v
	}

	return strings.TrimSpace(string(out))
}
