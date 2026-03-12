package controller

import (
	"context"
	"net/http"
	"time"

	"github.com/dxas90/pangolin-gateway-controller/pkg/pangolin"
)

type pangolinReadyChecker struct {
	client pangolin.ClientInterface
}

// NewPangolinReadyChecker returns a readiness check function that verifies
// connectivity to the Pangolin API by listing sites. Use this for the
// /readyz endpoint; keep /healthz as a simple ping.
func NewPangolinReadyChecker(client pangolin.ClientInterface) func(*http.Request) error {
	return (&pangolinReadyChecker{client: client}).check
}

func (p *pangolinReadyChecker) check(req *http.Request) error {
	ctx, cancel := context.WithTimeout(req.Context(), 5*time.Second)
	defer cancel()
	_, err := p.client.ListSites(ctx)
	return err
}
