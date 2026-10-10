package resume

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const exitTail = "\x1b[?25h\x1b7\x1b[r\x1b8\x1b]0;\a\x1b[2m\x1b[22m\r\n\x1b[2mResume this session with:\x1b[22m\r\n" +
	"\x1b[2mclaude --resume D8CB7E10-83cb-47c6-87ad-f23e5b07650f\x1b[22m\r\n\x1b[2m\x1b[22m\x1b[?25hlogout\r\n"

func writeLog(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "task.log")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestSessionIDReadsTheExitHint(t *testing.T) {
	path := writeLog(t, strings.Repeat("output ", 2000)+exitTail)

	if got := SessionID(path); got != "d8cb7e10-83cb-47c6-87ad-f23e5b07650f" {
		t.Fatalf("got %q", got)
	}
}

func TestSessionIDTakesTheLastHint(t *testing.T) {
	first := "Resume this session with:\r\nclaude --resume 2a3310d3-a725-4ef3-bbf4-b9ff5616bf3d\r\n"
	path := writeLog(t, first+"resumed output\r\n"+exitTail)

	if got := SessionID(path); got != "d8cb7e10-83cb-47c6-87ad-f23e5b07650f" {
		t.Fatalf("got %q", got)
	}
}

func TestSessionIDIgnoresAHintFarFromTheEnd(t *testing.T) {
	// a hint the conversation printed long before the agent exited without one
	path := writeLog(t, exitTail+strings.Repeat("later output ", 1000))

	if got := SessionID(path); got != "" {
		t.Fatalf("expected no session id, got %q", got)
	}
}

func TestSessionIDOfAMissingLogIsEmpty(t *testing.T) {
	if got := SessionID(filepath.Join(t.TempDir(), "missing.log")); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestRecordedOnlyAcceptsASessionNamedInTheLog(t *testing.T) {
	path := writeLog(t, "Resume this session with:\r\nclaude --resume 2a3310d3-a725-4ef3-bbf4-b9ff5616bf3d\r\n"+strings.Repeat("x", 10000))

	if !Recorded(path, "2a3310d3-a725-4ef3-bbf4-b9ff5616bf3d") {
		t.Error("expected the session named in the log to count as recorded")
	}
	if Recorded(path, "d8cb7e10-83cb-47c6-87ad-f23e5b07650f") {
		t.Error("a session the log never names must not count as recorded")
	}
	if Recorded(path, "2a3310d3' ; rm -rf ~") {
		t.Error("a malformed id must never count as recorded")
	}
}
