package controller

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/dxas90/pangolin-gateway-controller/pkg/pangolin"
)

type pangolinReadyChecker struct {
	client        pangolin.ClientInterface
	mu            sync.Mutex
	lastCheck     time.Time
	lastResult    error
	inFlight      bool          // true while a Ping is in progress
	cacheTTL      time.Duration // TTL for successful (nil) results
	errorCacheTTL time.Duration // shorter TTL for error results
}

// NewPangolinReadyChecker returns a readiness check function that verifies
// connectivity to the Pangolin API with a lightweight GET request, with a 30s
// result cache (5s on error) to avoid hammering the API on every probe.
// Concurrent probes while one is in-flight return the last cached result.
// Use this for the /readyz endpoint; keep /healthz as a simple process ping.
func NewPangolinReadyChecker(client pangolin.ClientInterface) func(*http.Request) error {
	return (&pangolinReadyChecker{
		client:        client,
		cacheTTL:      30 * time.Second,
		errorCacheTTL: 5 * time.Second,
	}).check
}

func (p *pangolinReadyChecker) check(req *http.Request) error {
	p.mu.Lock()
	ttl := p.cacheTTL
	if p.lastResult != nil {
		ttl = p.errorCacheTTL
	}
	if time.Since(p.lastCheck) < ttl || p.inFlight {
		result := p.lastResult
		p.mu.Unlock()
		return result
	}
	p.inFlight = true
	p.mu.Unlock()

	ctx, cancel := context.WithTimeout(req.Context(), 5*time.Second)
	defer cancel()
	err := p.client.Ping(ctx)

	p.mu.Lock()
	p.lastCheck = time.Now()
	p.lastResult = err
	p.inFlight = false
	p.mu.Unlock()

	return err
}
