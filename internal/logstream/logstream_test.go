package logstream

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func appendTo(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestStreamDeliversAppendedBytesInOrder(t *testing.T) {
	file := filepath.Join(t.TempDir(), "task.log")

	var mu sync.Mutex
	var seqs []int
	var builder strings.Builder
	send := func(seq int, chunk string) error {
		mu.Lock()
		defer mu.Unlock()
		seqs = append(seqs, seq)
		builder.WriteString(chunk)
		return nil
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		Stream(file, 5*time.Millisecond, send, stop)
		close(done)
	}()

	appendTo(t, file, "hello ")
	time.Sleep(30 * time.Millisecond)
	appendTo(t, file, "world")
	time.Sleep(30 * time.Millisecond)
	close(stop)
	<-done

	mu.Lock()
	defer mu.Unlock()
	if got := builder.String(); got != "hello world" {
		t.Fatalf("reassembled log = %q, want %q", got, "hello world")
	}
	for i, seq := range seqs {
		if seq != i+1 {
			t.Fatalf("seq[%d] = %d, want %d (seqs=%v)", i, seq, i+1, seqs)
		}
	}
}

func TestStreamRetriesSameChunkAfterFailure(t *testing.T) {
	file := filepath.Join(t.TempDir(), "task.log")
	appendTo(t, file, "chunk")

	var mu sync.Mutex
	var accepted []string
	var acceptedSeqs []int
	failNext := true
	send := func(seq int, chunk string) error {
		mu.Lock()
		defer mu.Unlock()
		if failNext {
			failNext = false
			return errors.New("transient")
		}
		acceptedSeqs = append(acceptedSeqs, seq)
		accepted = append(accepted, chunk)
		return nil
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		Stream(file, 5*time.Millisecond, send, stop)
		close(done)
	}()

	time.Sleep(40 * time.Millisecond)
	close(stop)
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(accepted) != 1 || accepted[0] != "chunk" {
		t.Fatalf("accepted chunks = %v, want [chunk]", accepted)
	}
	if len(acceptedSeqs) != 1 || acceptedSeqs[0] != 1 {
		t.Fatalf("accepted seqs = %v, want [1] (seq must not advance on failure)", acceptedSeqs)
	}
}
