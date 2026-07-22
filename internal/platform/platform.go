// Package platform reports the runner's operating system and architecture in
// the vocabulary the foreman server expects, and rejects unsupported targets.
package platform

import (
	"fmt"
	"runtime"
)

// Detect returns the current OS and architecture, constrained to the platforms
// foreman supports: linux/darwin and amd64/arm64.
func Detect() (os string, arch string, err error) {
	os = runtime.GOOS
	arch = runtime.GOARCH

	switch os {
	case "linux", "darwin":
	default:
		return "", "", fmt.Errorf("unsupported OS %q (supported: linux, darwin)", os)
	}

	switch arch {
	case "amd64", "arm64":
	default:
		return "", "", fmt.Errorf("unsupported architecture %q (supported: amd64, arm64)", arch)
	}

	return os, arch, nil
}
