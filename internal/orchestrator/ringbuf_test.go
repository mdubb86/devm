package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLineRingBufferKeepsLastN(t *testing.T) {
	b := newLineRingBuffer(3)
	for i := 1; i <= 5; i++ {
		b.Write([]byte("line " + string(rune('0'+i)) + "\n"))
	}
	assert.Equal(t, "line 3\nline 4\nline 5\n", b.String())
}

func TestLineRingBufferKeepsTrailingPartial(t *testing.T) {
	b := newLineRingBuffer(10)
	b.Write([]byte("complete\n"))
	b.Write([]byte("partial-no-nl"))
	assert.Equal(t, "complete\npartial-no-nl", b.String())
}

func TestLineRingBufferChunkedWrites(t *testing.T) {
	// Writes that span line boundaries must still be split correctly.
	b := newLineRingBuffer(10)
	b.Write([]byte("hel"))
	b.Write([]byte("lo\nwor"))
	b.Write([]byte("ld\n"))
	assert.Equal(t, "hello\nworld\n", b.String())
}

func TestLineRingBufferEmpty(t *testing.T) {
	b := newLineRingBuffer(10)
	assert.True(t, b.IsEmpty())
	assert.Equal(t, "", b.String())
	b.Write([]byte("x"))
	assert.False(t, b.IsEmpty())
}
