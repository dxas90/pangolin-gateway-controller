package controller

import (
	"testing"
	"time"

	"github.com/dxas90/pangolin-gateway-controller/pkg/config"
	"github.com/stretchr/testify/assert"
)

func TestBuildRateLimiter_DefaultConfig(t *testing.T) {
	cfg := &config.ControllerConfig{
		RateLimiterBaseDelay: 5 * time.Millisecond,
		RateLimiterMaxDelay:  1000 * time.Second,
		WorkqueueQPS:         10.0,
		WorkqueueBurst:       100,
	}

	limiter := buildRateLimiter(cfg)
	assert.NotNil(t, limiter)
}

func TestBuildRateLimiter_CustomConfig(t *testing.T) {
	cfg := &config.ControllerConfig{
		RateLimiterBaseDelay: 100 * time.Millisecond,
		RateLimiterMaxDelay:  60 * time.Second,
		WorkqueueQPS:         50.0,
		WorkqueueBurst:       200,
	}

	limiter := buildRateLimiter(cfg)
	assert.NotNil(t, limiter)
}

func TestBuildRateLimiter_MinimalConfig(t *testing.T) {
	cfg := &config.ControllerConfig{
		RateLimiterBaseDelay: 1 * time.Millisecond,
		RateLimiterMaxDelay:  1 * time.Second,
		WorkqueueQPS:         1.0,
		WorkqueueBurst:       1,
	}

	limiter := buildRateLimiter(cfg)
	assert.NotNil(t, limiter)
}
