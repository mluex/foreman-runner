// Package system samples lightweight host metrics for heartbeats.
package system

import (
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/mluex/foreman-runner/internal/api"
)

// Sample returns current host metrics. Values are best-effort and default to
// zero on platforms where they are not read (currently non-Linux).
func Sample() api.System {
	return api.System{
		Load1:     load1(),
		MemFreeMB: memFreeMB(),
	}
}

func load1() float64 {
	if runtime.GOOS != "linux" {
		return 0
	}
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(fields[0], 64)
	return v
}

func memFreeMB() int64 {
	if runtime.GOOS != "linux" {
		return 0
	}
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "MemAvailable:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			kb, _ := strconv.ParseInt(fields[1], 10, 64)
			return kb / 1024
		}
	}
	return 0
}
