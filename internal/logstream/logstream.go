// Package logstream tails a task's captured tmux log file and ships newly
// appended output to the foreman server in ordered, deduplicated chunks.
package logstream

import (
	"io"
	"os"
	"time"
)

// SendFunc delivers one ordered chunk. seq is a per-task monotonic sequence
// starting at 1. It must return a non-nil error when the chunk was not
// accepted, so the streamer can retry the same seq and bytes.
type SendFunc func(seq int, chunk string) error

// Stream tails logFile, batching newly appended bytes every interval and
// delivering them through send. It returns after stop is closed and a final
// best-effort flush.
//
// A chunk that fails to send is retried unchanged on the next tick, so a given
// seq always maps to the same bytes; combined with the server's dedup on
// (task, seq), delivery is at-least-once without gaps or reordering.
func Stream(logFile string, interval time.Duration, send SendFunc, stop <-chan struct{}) {
	var (
		offset     int64
		seq        int
		pending    []byte
		pendingSeq int
	)

	flush := func() {
		if pending == nil {
			data, err := readFrom(logFile, offset)
			if err != nil || len(data) == 0 {
				return
			}
			seq++
			pending = data
			pendingSeq = seq
		}
		if err := send(pendingSeq, string(pending)); err != nil {
			return // keep pending; retry the same seq and bytes next tick
		}
		offset += int64(len(pending))
		pending = nil
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			flush()
		case <-stop:
			flush()
			return
		}
	}
}

// readFrom returns the bytes of file after offset. A file that does not exist
// yet (the agent has produced no output) yields no bytes and no error.
func readFrom(file string, offset int64) ([]byte, error) {
	f, err := os.Open(file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}
