// Package resume reads the agent session a task ran in from the task's log,
// so an ended task can be reopened in the same conversation. Claude Code
// prints "Resume this session with: claude --resume <id>" when it exits; that
// hint is the only place the runner learns the id.
package resume

import (
	"bytes"
	"io"
	"os"
	"regexp"
	"strings"
)

// tailBytes is how much of the end of a log is searched for the exit hint. The
// hint is the last thing the agent prints, so a short tail keeps ids the
// conversation itself happened to print from being mistaken for it.
const tailBytes = 4096

var (
	sessionID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	exitHint  = regexp.MustCompile(`Resume this session with:\s*claude --resume ([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})`)
	// CSI sequences, OSC sequences, then any other two-byte escape
	escapes = regexp.MustCompile("\x1b(?:\\[[0-?]*[ -/]*[@-~]|\\][^\x07\x1b]*(?:\x07|\x1b\\\\)|.)")
)

// SessionID returns the session id from the exit hint at the end of a log, or
// "" when the log does not end with one (an agent killed hard, or one that
// prints no hint).
func SessionID(logFile string) string {
	f, err := os.Open(logFile)
	if err != nil {
		return ""
	}
	defer f.Close()

	if info, statErr := f.Stat(); statErr == nil && info.Size() > tailBytes {
		if _, seekErr := f.Seek(-tailBytes, io.SeekEnd); seekErr != nil {
			return ""
		}
	}
	tail, err := io.ReadAll(f)
	if err != nil {
		return ""
	}

	matches := exitHint.FindAllSubmatch(escapes.ReplaceAll(tail, nil), -1)
	if len(matches) == 0 {
		return ""
	}

	return strings.ToLower(string(matches[len(matches)-1][1]))
}

// Valid reports whether id has the shape of an agent session id.
func Valid(id string) bool {
	return sessionID.MatchString(id)
}

// Recorded reports whether the agent named id as its session in this log at
// some point. A resume is only started for a session the task itself ran in,
// so the server cannot point a task at some other conversation on this machine.
func Recorded(logFile, id string) bool {
	if !Valid(id) {
		return false
	}
	data, err := os.ReadFile(logFile)
	if err != nil {
		return false
	}

	return bytes.Contains(data, []byte("claude --resume "+id))
}
