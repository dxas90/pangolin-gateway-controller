package controller

import (
	"github.com/dxas90/pangolin-gateway-controller/pkg/config"
	"golang.org/x/time/rate"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// buildRateLimiter returns a composite TypedRateLimiter that combines:
//   - An exponential failure back-off (base/max from config) to handle per-item retry storms.
//   - A token-bucket limiter (QPS + burst from config) to cap the overall workqueue throughput.
func buildRateLimiter(cfg *config.ControllerConfig) workqueue.TypedRateLimiter[reconcile.Request] {
	return workqueue.NewTypedMaxOfRateLimiter(
		workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](
			cfg.RateLimiterBaseDelay,
			cfg.RateLimiterMaxDelay,
		),
		&workqueue.TypedBucketRateLimiter[reconcile.Request]{
			Limiter: rate.NewLimiter(rate.Limit(cfg.WorkqueueQPS), cfg.WorkqueueBurst),
		},
	)
}
