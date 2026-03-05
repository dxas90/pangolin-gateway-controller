package pangolin

import (
	"fmt"
	"sync"
	"time"
)

// ErrCircuitOpen is returned when the circuit breaker is open and requests are fast-failed.
var ErrCircuitOpen = fmt.Errorf("pangolin API circuit breaker is open: too many consecutive failures")

type circuitState int

const (
	stateClosed   circuitState = iota // Normal operation; requests pass through
	stateOpen                         // API degraded; requests fail fast
	stateHalfOpen                     // Probe state after timeout; one request allowed
)

// CircuitBreaker prevents cascading failures when the Pangolin API is degraded.
// Transitions:
//
//	CLOSED → (threshold retryable failures) → OPEN
//	OPEN   → (timeout elapsed)              → HALF_OPEN
//	HALF_OPEN → (success)                   → CLOSED
//	HALF_OPEN → (failure)                   → OPEN
type CircuitBreaker struct {
	mu          sync.Mutex
	state       circuitState
	failures    int
	threshold   int
	timeout     time.Duration
	lastFailure time.Time
}

// NewCircuitBreaker creates a CircuitBreaker that opens after `threshold` consecutive
// retryable failures and attempts recovery after `timeout`.
func NewCircuitBreaker(threshold int, timeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		threshold: threshold,
		timeout:   timeout,
		state:     stateClosed,
	}
}

// Allow returns ErrCircuitOpen if requests should be fast-failed.
// When transitioning OPEN → HALF_OPEN (timeout elapsed) one probe request passes through.
func (cb *CircuitBreaker) Allow() error {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == stateOpen {
		if time.Since(cb.lastFailure) < cb.timeout {
			return ErrCircuitOpen
		}
		// Timeout elapsed — allow probe request
		cb.state = stateHalfOpen
	}
	return nil
}

// RecordSuccess resets failure count and returns the circuit to CLOSED.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failures = 0
	cb.state = stateClosed
}

// RecordFailure records a retryable failure and opens the circuit when the threshold is reached.
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failures++
	cb.lastFailure = time.Now()
	if cb.failures >= cb.threshold {
		cb.state = stateOpen
	}
}

// State returns the current state name (for logging/metrics).
func (cb *CircuitBreaker) State() string {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	switch cb.state {
	case stateClosed:
		return "closed"
	case stateOpen:
		return "open"
	case stateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}
