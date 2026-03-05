package pangolin

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCircuitBreaker_InitiallyClosed(t *testing.T) {
	cb := NewCircuitBreaker(3, 10*time.Second)
	assert.Equal(t, "closed", cb.State())
	require.NoError(t, cb.Allow())
}

func TestCircuitBreaker_OpensAfterThreshold(t *testing.T) {
	cb := NewCircuitBreaker(3, 10*time.Second)

	cb.RecordFailure()
	assert.Equal(t, "closed", cb.State())
	cb.RecordFailure()
	assert.Equal(t, "closed", cb.State())
	cb.RecordFailure() // Threshold reached
	assert.Equal(t, "open", cb.State())
}

func TestCircuitBreaker_RejectsRequestsWhenOpen(t *testing.T) {
	cb := NewCircuitBreaker(1, 10*time.Minute)
	cb.RecordFailure() // Open the circuit
	assert.Equal(t, "open", cb.State())

	err := cb.Allow()
	assert.ErrorIs(t, err, ErrCircuitOpen)
}

func TestCircuitBreaker_TransitionsToHalfOpenAfterTimeout(t *testing.T) {
	cb := NewCircuitBreaker(1, 1*time.Millisecond)
	cb.RecordFailure() // Open the circuit
	assert.Equal(t, "open", cb.State())

	time.Sleep(5 * time.Millisecond) // Wait past timeout

	// Allow() should transition to half-open and return nil
	err := cb.Allow()
	assert.NoError(t, err)
	assert.Equal(t, "half-open", cb.State())
}

func TestCircuitBreaker_SuccessFromHalfOpenCloses(t *testing.T) {
	cb := NewCircuitBreaker(1, 1*time.Millisecond)
	cb.RecordFailure()
	time.Sleep(5 * time.Millisecond)

	// Transition to half-open
	require.NoError(t, cb.Allow())
	assert.Equal(t, "half-open", cb.State())

	// Success should close the circuit
	cb.RecordSuccess()
	assert.Equal(t, "closed", cb.State())
	assert.NoError(t, cb.Allow())
}

func TestCircuitBreaker_FailureFromHalfOpenReopens(t *testing.T) {
	cb := NewCircuitBreaker(1, 1*time.Millisecond)
	cb.RecordFailure()
	time.Sleep(5 * time.Millisecond)

	// Transition to half-open
	require.NoError(t, cb.Allow())

	// Another failure reopens the circuit
	cb.RecordFailure()
	assert.Equal(t, "open", cb.State())
	assert.ErrorIs(t, cb.Allow(), ErrCircuitOpen)
}

func TestCircuitBreaker_SuccessResetsFailureCount(t *testing.T) {
	cb := NewCircuitBreaker(3, 10*time.Second)

	cb.RecordFailure()
	cb.RecordFailure()
	assert.Equal(t, "closed", cb.State())

	// Success resets
	cb.RecordSuccess()

	// Two more failures should not open (counter was reset)
	cb.RecordFailure()
	cb.RecordFailure()
	assert.Equal(t, "closed", cb.State())

	// Third failure opens
	cb.RecordFailure()
	assert.Equal(t, "open", cb.State())
}

func TestCircuitBreaker_StillBlocksWithinTimeout(t *testing.T) {
	cb := NewCircuitBreaker(1, 1*time.Hour) // Very long timeout
	cb.RecordFailure()
	assert.Equal(t, "open", cb.State())

	// Should still be blocked
	assert.ErrorIs(t, cb.Allow(), ErrCircuitOpen)
	assert.ErrorIs(t, cb.Allow(), ErrCircuitOpen)
}
