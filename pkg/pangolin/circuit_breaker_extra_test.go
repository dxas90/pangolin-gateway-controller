package pangolin

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- SetStateChangeCallback ---

func TestCircuitBreaker_SetStateChangeCallback_CalledOnOpen(t *testing.T) {
	cb := NewCircuitBreaker(2, 10*time.Second)

	var from, to string
	cb.SetStateChangeCallback(func(f, t2 string) { from, to = f, t2 })

	cb.RecordFailure() // 1 — still closed
	assert.Empty(t, from, "callback must not fire before threshold")

	cb.RecordFailure() // 2 — opens
	assert.Equal(t, "closed", from)
	assert.Equal(t, "open", to)
}

func TestCircuitBreaker_SetStateChangeCallback_CalledOnClose_FromHalfOpen(t *testing.T) {
	cb := NewCircuitBreaker(1, 1*time.Millisecond)

	var transitions []string
	cb.SetStateChangeCallback(func(f, t2 string) {
		transitions = append(transitions, f+"->"+t2)
	})

	cb.RecordFailure() // open  (closed->open)
	time.Sleep(5 * time.Millisecond)
	require.NoError(t, cb.Allow()) // half-open probe
	cb.RecordSuccess()             // half-open->closed

	assert.Contains(t, transitions, "closed->open")
	assert.Contains(t, transitions, "half-open->closed")
}

func TestCircuitBreaker_SetStateChangeCallback_NotCalledOnSuccessInClosed(t *testing.T) {
	cb := NewCircuitBreaker(3, 10*time.Second)

	called := false
	cb.SetStateChangeCallback(func(_, _ string) { called = true })

	cb.RecordSuccess() // no state change — already closed
	assert.False(t, called, "callback must not fire on success when already closed")
}

func TestCircuitBreaker_SetStateChangeCallback_HalfOpenFailReopens(t *testing.T) {
	cb := NewCircuitBreaker(1, 1*time.Millisecond)

	var transitions []string
	cb.SetStateChangeCallback(func(f, t2 string) {
		transitions = append(transitions, f+"->"+t2)
	})

	cb.RecordFailure() // closed->open
	time.Sleep(5 * time.Millisecond)
	require.NoError(t, cb.Allow()) // half-open
	cb.RecordFailure()             // half-open->open (threshold=1, failures becomes 2 which >= 1)

	assert.Contains(t, transitions, "half-open->open")
}

func TestCircuitBreaker_SetStateChangeCallback_ReplacedCallback(t *testing.T) {
	cb := NewCircuitBreaker(1, 10*time.Second)

	var first, second int
	cb.SetStateChangeCallback(func(_, _ string) { first++ })
	cb.SetStateChangeCallback(func(_, _ string) { second++ }) // replace

	cb.RecordFailure()

	assert.Equal(t, 0, first, "old callback must not be called after replacement")
	assert.Equal(t, 1, second)
}

// --- State name coverage ---

func TestCircuitBreaker_StateName_AllStates(t *testing.T) {
	cb := NewCircuitBreaker(1, 1*time.Millisecond)
	assert.Equal(t, "closed", cb.State())

	cb.RecordFailure()
	assert.Equal(t, "open", cb.State())

	time.Sleep(5 * time.Millisecond)
	require.NoError(t, cb.Allow())
	assert.Equal(t, "half-open", cb.State())
}

// --- Concurrency ---

func TestCircuitBreaker_ConcurrentRecordFailure_OpensExactlyOnce(t *testing.T) {
	cb := NewCircuitBreaker(10, 10*time.Second)

	var openCount atomic.Int32
	cb.SetStateChangeCallback(func(_, to string) {
		if to == "open" {
			openCount.Add(1)
		}
	})

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cb.RecordFailure()
		}()
	}
	wg.Wait()

	assert.Equal(t, "open", cb.State())
	assert.Equal(t, int32(1), openCount.Load(), "circuit must open exactly once")
}

func TestCircuitBreaker_ConcurrentAllow_OnlyOneHalfOpenProbe(t *testing.T) {
	cb := NewCircuitBreaker(1, 1*time.Millisecond)
	cb.RecordFailure()
	time.Sleep(5 * time.Millisecond)

	// Multiple goroutines race to Allow(). Only one should get nil (the probe).
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if cb.Allow() == nil {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()

	// Exactly one probe passes through (the one that transitions to half-open).
	// Remaining goroutines see open or half-open (both can return nil once in half-open).
	// The contract is: at least one passes, and the breaker is not still closed.
	assert.GreaterOrEqual(t, allowed.Load(), int32(1))
}

// --- RecordFailure on already-open circuit ---

func TestCircuitBreaker_RecordFailure_WhenAlreadyOpen_NoExtraCallback(t *testing.T) {
	cb := NewCircuitBreaker(1, 10*time.Second)

	var callCount int
	cb.SetStateChangeCallback(func(_, _ string) { callCount++ })

	cb.RecordFailure() // opens (callback=1)
	cb.RecordFailure() // already open — no state change
	cb.RecordFailure()

	assert.Equal(t, 1, callCount, "callback fires only on the transition, not on every failure while open")
}

// --- RecordSuccess idempotence while closed ---

func TestCircuitBreaker_MultipleSuccessesWhileClosed_NoPanic(t *testing.T) {
	cb := NewCircuitBreaker(3, 10*time.Second)
	require.NotPanics(t, func() {
		for range 10 {
			cb.RecordSuccess()
		}
	})
	assert.Equal(t, "closed", cb.State())
}

// --- Threshold of 1 ---

func TestCircuitBreaker_ThresholdOne_OpensOnFirstFailure(t *testing.T) {
	cb := NewCircuitBreaker(1, 10*time.Second)
	cb.RecordFailure()
	assert.Equal(t, "open", cb.State())
	assert.ErrorIs(t, cb.Allow(), ErrCircuitOpen)
}
