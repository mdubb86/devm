package serviceapi

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEgressPassthroughStore_PutGetRoundTrip(t *testing.T) {
	s := newEgressPassthroughStore()
	deadline := time.Now().Add(30 * time.Second)
	s.put("p1", deadline)

	entry, ok := s.get("p1")
	require.True(t, ok, "get after put must return the entry")
	assert.Equal(t, deadline, entry.expiresAt)
	assert.Nil(t, entry.restore, "put alone must not install a timer")
}

func TestEgressPassthroughStore_GetMissing_ReturnsFalse(t *testing.T) {
	s := newEgressPassthroughStore()
	_, ok := s.get("nope")
	assert.False(t, ok)
}

func TestEgressPassthroughStore_SetTimer_ReplacesPrevious(t *testing.T) {
	s := newEgressPassthroughStore()
	s.put("p1", time.Now().Add(time.Hour))

	var firedFirst atomic.Int32
	t1 := time.AfterFunc(10*time.Millisecond, func() { firedFirst.Add(1) })
	s.setTimer("p1", t1)

	// Replace before it can fire; the first timer must not fire after
	// the second is installed.
	var firedSecond atomic.Int32
	t2 := time.AfterFunc(50*time.Millisecond, func() { firedSecond.Add(1) })
	s.setTimer("p1", t2)

	time.Sleep(100 * time.Millisecond)
	assert.EqualValues(t, 0, firedFirst.Load(), "replaced timer must be stopped, not fire")
	assert.EqualValues(t, 1, firedSecond.Load(), "replacement timer must fire")
}

func TestEgressPassthroughStore_StopTimer_Cancels(t *testing.T) {
	s := newEgressPassthroughStore()
	s.put("p1", time.Now().Add(time.Hour))

	var fired atomic.Int32
	t1 := time.AfterFunc(50*time.Millisecond, func() { fired.Add(1) })
	s.setTimer("p1", t1)

	s.stopTimer("p1")
	time.Sleep(100 * time.Millisecond)
	assert.EqualValues(t, 0, fired.Load(), "stopTimer must cancel the pending fire")

	entry, ok := s.get("p1")
	require.True(t, ok, "stopTimer must not delete the entry")
	assert.Nil(t, entry.restore, "stopTimer must clear the timer field")
}

func TestEgressPassthroughStore_Del_StopsTimerAndRemovesEntry(t *testing.T) {
	s := newEgressPassthroughStore()
	s.put("p1", time.Now().Add(time.Hour))

	var fired atomic.Int32
	t1 := time.AfterFunc(50*time.Millisecond, func() { fired.Add(1) })
	s.setTimer("p1", t1)

	s.del("p1")

	_, ok := s.get("p1")
	assert.False(t, ok, "del must remove the entry")

	time.Sleep(100 * time.Millisecond)
	assert.EqualValues(t, 0, fired.Load(), "del must also cancel the pending timer")
}

func TestEgressPassthroughStore_DefaultDurationConst(t *testing.T) {
	// Pin the spec's default: `devm passthrough` (no --for) opens a
	// 30-second window. Longer defaults raise the security exposure;
	// shorter ones make the user re-invoke mid-supervision.
	assert.Equal(t, 30, defaultPassthroughSeconds)
}
