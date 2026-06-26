package orchestrator

import (
	"bytes"
	"strings"
	"sync"
)

// lineRingBuffer is a thread-safe io.Writer that retains the last N
// lines of whatever's written to it. Used to capture output from
// background processes so failures can surface the relevant error
// message in the returned error — without unbounded memory growth or a
// file on disk.
//
// Bytes are buffered as they arrive and split on '\n'. A trailing
// partial line (no terminating newline) is also retained and emitted
// by String().
type lineRingBuffer struct {
	mu      sync.Mutex
	lines   []string // last `cap` complete lines, oldest first
	cap     int
	partial []byte // bytes since the last '\n', not yet a complete line
}

func newLineRingBuffer(cap int) *lineRingBuffer {
	return &lineRingBuffer{cap: cap}
}

func (b *lineRingBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.partial = append(b.partial, p...)
	for {
		i := bytes.IndexByte(b.partial, '\n')
		if i < 0 {
			break
		}
		line := string(b.partial[:i])
		// Compact the buffer — copy remaining bytes to the front so
		// we don't accumulate large slices over the process's lifetime.
		b.partial = append(b.partial[:0], b.partial[i+1:]...)
		b.lines = append(b.lines, line)
		if len(b.lines) > b.cap {
			b.lines = b.lines[len(b.lines)-b.cap:]
		}
	}
	return len(p), nil
}

// String returns the retained lines joined by '\n' plus any trailing
// partial line. Safe to call concurrently with Write.
func (b *lineRingBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out strings.Builder
	for _, l := range b.lines {
		out.WriteString(l)
		out.WriteByte('\n')
	}
	if len(b.partial) > 0 {
		out.Write(b.partial)
	}
	return out.String()
}

// IsEmpty reports whether anything has been written.
func (b *lineRingBuffer) IsEmpty() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.lines) == 0 && len(b.partial) == 0
}
