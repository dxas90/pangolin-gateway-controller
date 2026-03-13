package controller

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/dxas90/pangolin-gateway-controller/pkg/pangolin"
)

type pangolinReadyChecker struct {
	client     pangolin.ClientInterface
	mu         sync.Mutex
	lastCheck  time.Time
	lastResult error
	cacheTTL   time.Duration
}

// NewPangolinReadyChecker returns a readiness check function that verifies
// connectivity to the Pangolin API by listing sites, with a 30s result cache
// to avoid hammering the API on every probe. Use this for the /readyz endpoint;
// keep /healthz as a simple ping.
func NewPangolinReadyChecker(client pangolin.ClientInterface) func(*http.Request) error {
	return (&pangolinReadyChecker{client: client, cacheTTL: 30 * time.Second}).check
}

func (p *pangolinReadyChecker) check(req *http.Request) error {
	p.mu.Lock()
	if time.Since(p.lastCheck) < p.cacheTTL {
		result := p.lastResult
		p.mu.Unlock()
		return result
	}
	p.mu.Unlock()

	ctx, cancel := context.WithTimeout(req.Context(), 5*time.Second)
	defer cancel()
	_, err := p.client.ListSites(ctx)

	p.mu.Lock()
	p.lastCheck = time.Now()
	p.lastResult = err
	p.mu.Unlock()

	return err
}
