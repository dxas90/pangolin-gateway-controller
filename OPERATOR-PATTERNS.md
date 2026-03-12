# Operator Best Practices Implementation

This document tracks the implementation of production-grade operator patterns from "Kubernetes Operators Explained by a Production Engineer" (HackerNoon article).

## ✅ Implemented Best Practices

### 1. Status Conditions with observedGeneration ✅

**Pattern:** Multi-dimensional state communication through typed conditions

**Implementation:**

- File: `pkg/controller/gateway_controller.go`
- All status updates include `ObservedGeneration` field
- Conditions follow Kubernetes Deployment pattern (Type, Status, Reason, Message)
- External observers can determine if controller has processed latest spec

```go
condition := metav1.Condition{
    Type:               string(condType),
    Status:             metav1.ConditionTrue,
    Reason:             reason,
    Message:            message,
    ObservedGeneration: fresh.Generation,  // ✅ Tracks spec version
    LastTransitionTime: metav1.Now(),
}
```

**Benefits:**

- Clients can detect stale status
- Avoids ambiguity in resource state
- Standard Kubernetes pattern

---

### 2. Separate Status Updates (Status().Update) ✅

**Pattern:** Use dedicated status subresource endpoint

**Implementation:**

- All controllers use `r.Status().Update(ctx, obj)` not `r.Update(ctx, obj)`
- Status has separate API endpoint and RBAC policies
- Prevents spec/status coupling issues

```go
// ✅ Correct - uses status subresource
return r.Status().Update(ctx, fresh)

// ❌ Wrong - updates both spec and status
return r.Update(ctx, fresh)
```

**Benefits:**

- Proper RBAC separation
- Follows Kubernetes API conventions
- Prevents accidental spec modifications

---

### 3. Re-fetch Before Update (Conflict Handling) ✅

**Pattern:** Always re-read resource before updating to handle conflicts

**Implementation:**

- File: `pkg/controller/gateway_controller.go:updateGatewayStatus()`
- Uses `retry.RetryOnConflict()` with fresh resource fetch
- Prevents 409 Conflict errors from stale resourceVersion

```go
err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
    // Re-fetch to get latest version
    fresh := &gatewayv1.Gateway{}
    if err := r.Get(ctx, key, fresh); err != nil {
        return err
    }

    // Update status on fresh copy
    fresh.Status.Conditions = ...
    return r.Status().Update(ctx, fresh)
})
```

**Benefits:**

- Handles concurrent updates gracefully
- Reduces reconciliation failures
- Production-grade conflict resolution

---

### 4. Field Indexing for Efficient Lookups ✅

**Pattern:** Index frequently-filtered fields to convert O(n) scans to O(1) lookups

**Implementation:**

- File: `pkg/controller/indexes.go`
- Indexes Gateway by GatewayClassName
- Indexes HTTPRoute/GRPCRoute by parent Gateway references

```go
// Setup indexes at controller startup
mgr.GetFieldIndexer().IndexField(
    ctx,
    &gatewayv1.Gateway{},
    ".spec.gatewayClassName",
    func(obj client.Object) []string {
        return []string{string(obj.(*gatewayv1.Gateway).Spec.GatewayClassName)}
    },
)

// Use indexed field in queries
r.List(ctx, &gateways, client.MatchingFields{
    ".spec.gatewayClassName": "pangolin",  // O(1) lookup!
})
```

**Benefits:**

- Dramatically faster queries
- Reduced API server load
- Scalable to thousands of resources

---

### 5. Controller Reference Ownership ✅

**Pattern:** Set owner references for garbage collection

**Implementation:**

- File: `pkg/controller/newt_controller.go`
- All child resources (Deployments, Services, Secrets) have owner references
- Kubernetes garbage collector handles cleanup automatically

```go
OwnerReferences: []metav1.OwnerReference{
    {
        APIVersion: gateway.APIVersion,
        Kind:       gateway.Kind,
        Name:       gateway.Name,
        UID:        gateway.UID,
        Controller: ptr(true),  // ✅ Marks as controller owner
    },
}
```

**Benefits:**

- Automatic cleanup on parent deletion
- No orphaned child resources
- Standard Kubernetes pattern

---

### 6. MaxConcurrentReconciles for Parallelism ✅

**Pattern:** Enable parallel reconciliation across different objects

**Implementation:**

- Files: `pkg/controller/*_controller.go` (all controllers)
- Gateway: 3 concurrent reconciles
- HTTPRoute: 5 concurrent reconciles (more numerous)
- GRPCRoute: 3 concurrent reconciles

```go
func (r *HTTPRouteReconciler) SetupWithManager(mgr ctrl.Manager) error {
    return ctrl.NewControllerManagedBy(mgr).
        For(&gatewayv1.HTTPRoute{}).
        WithOptions(ctrl.Options{
            MaxConcurrentReconciles: 5,  // ✅ Parallel processing
        }).
        Complete(r)
}
```

**Benefits:**

- Higher throughput
- Reduced reconciliation latency
- Scales to hundreds of resources

---

### 7. Prometheus Metrics ✅

**Pattern:** Expose controller runtime and domain metrics

**Implementation:**

- File: `pkg/metrics/metrics.go`
- Controller metrics: reconcile_total, reconcile_duration
- API metrics: request duration, errors, latency
- Resource metrics: gateway_total, httproute_total, sites_total

```go
// Reconciliation metrics
ReconcileTotal.WithLabelValues("gateway", "success").Inc()
ReconcileDuration.WithLabelValues("gateway").Observe(duration)

// API metrics (auto-instrumented in client)
PangolinAPILatency.WithLabelValues(path, method).Observe(latency)
```

**Benefits:**

- Observability into controller health
- SLI/SLO tracking
- Alerting on anomalies

---

### 8. Typed Error Handling ✅

**Pattern:** Rich error context for intelligent retry decisions

**Implementation:**

- File: `pkg/pangolin/errors.go`
- `PangolinAPIError` type with HTTP context
- Helper methods: `IsRetryable()`, `IsNotFound()`, `IsConflict()`

```go
type PangolinAPIError struct {
    StatusCode int
    Endpoint   string
    Message    string
    Method     string
}

func (e *PangolinAPIError) IsRetryable() bool {
    return e.StatusCode >= 500 || e.StatusCode == 429
}
```

**Benefits:**

- Better debugging with context
- Intelligent retry logic
- Structured error information

---

### 9. Idempotent Reconciliation ✅

**Pattern:** Reconcilers can run multiple times safely

**Implementation:**

- All controllers check resource existence before creation
- Use declarative configurations
- Status updates are idempotent

**Examples:**

- Gateway: Checks if Pangolin site exists before creating
- HTTPRoute: Checks if resource exists by subdomain before creating
- Newt: Checks if Deployment exists before creating

**Benefits:**

- Resilient to missed events
- Safe to rerun reconciliation
- Level-triggered behavior

---

### 10. Finalizer Best Practices ✅

**Pattern:** Robust cleanup with operational runbooks

**Implementation:**

- File: `RUNBOOK.md` (complete operational procedures)
- Finalizers documented with removal procedures
- Emergency procedures for stuck finalizers
- Manual cleanup instructions

**Sections:**

- Normal finalizer flow
- Stuck finalizer detection
- Manual cleanup procedures
- Force removal (emergency only)
- Bulk operations

**Benefits:**

- Clear operational guidance
- Prevents permanent deletion blocks
- Incident response procedures

---

## 📋 Documented for Future Implementation

### 11. Webhook Validation (Documented)

**Pattern:** Fast failure on invalid configs at admission time

**Status:** Design documented in `IMPROVEMENTS.md`

**Proposed:** ValidatingWebhookConfiguration for Gateway/HTTPRoute

---

## 🎯 Comparative Analysis

| Best Practice | Article | Our Implementation | Status |
| ------------- | ------- | ----------------- | ------ |
| Control Theory (Level-Triggered) | ✅ | ✅ Implicit in reconcile pattern | ✅ Complete |
| Idempotency | ✅ | ✅ Existence checks before create | ✅ Complete |
| Error Handling | ✅ | ✅ Typed errors + retry.RetryOnConflict | ✅ Complete |
| Re-fetch Before Update | ✅ | ✅ In status update function | ✅ Complete |
| Status Conditions | ✅ | ✅ With observedGeneration | ✅ Complete |
| Separate Status Updates | ✅ | ✅ Status().Update() everywhere | ✅ Complete |
| MaxConcurrentReconciles | ✅ | ✅ 3-5 per controller | ✅ Complete |
| Cache Performance | ✅ | ✅ Field indexing added | ✅ Complete |
| Controller References | ✅ | ✅ On all child resources | ✅ Complete |
| Finalizers | ✅ | ✅ With runbook | ✅ Complete |
| Conflict Retry | ✅ | ✅ retry.RetryOnConflict | ✅ Complete |
| Leader Election | ✅ | ✅ Configured in main.go | ✅ Complete |
| RBAC Least Privilege | ✅ | ✅ Generated by kubebuilder | ✅ Complete |
| Metrics | ✅ | ✅ 10+ metrics exposed | ✅ Complete |
| Graceful Shutdown | ✅ | ✅ terminationGracePeriodSeconds | ✅ Complete |
| Circuit Breaker | ✅ | ✅ Custom circuit breaker in circuit_breaker.go | ✅ Complete |
| Events | ✅ | ✅ recorder.Eventf() throughout all controllers | ✅ Complete |
| Config Validation | ✅ | ✅ Validate() called at startup | ✅ Complete |
| Exponential Rate Limiter | ✅ | ✅ ratelimiter.go (token bucket + exp backoff) | ✅ Complete |
| Enhanced Health Check | ✅ | ✅ /readyz checks live Pangolin API | ✅ Complete |
| Build-time Version Injection | ✅ | ✅ -ldflags in Makefile | ✅ Complete |
| Code Organization | ✅ | ✅ httproute split into 4 files | ✅ Complete |
| Semver Releases | ✅ | ✅ CHANGELOG.md + VERSION in Makefile | ✅ Complete |
| Webhooks | ✅ | 📋 Documented | 📋 Future |

## 📊 Compliance Score

## 23 / 24 best practices implemented (96%)

Remaining practice (webhooks) is documented with implementation guide in `IMPROVEMENTS.md`.

---

## 🚀 Production Readiness Checklist

- [x] Status conditions with observedGeneration
- [x] Separate status updates (Status().Update)
- [x] Conflict retry pattern
- [x] Field indexing for performance
- [x] Controller owner references
- [x] Concurrent reconciliation
- [x] Prometheus metrics
- [x] Typed error handling
- [x] Idempotent reconciliation
- [x] Finalizer runbook
- [x] Leader election
- [x] RBAC least privilege
- [x] Graceful shutdown
- [x] Circuit breaker (custom implementation)
- [x] Structured events (recorder.Eventf)
- [x] Config validation at startup
- [x] Exponential rate limiter
- [x] Enhanced health check (/readyz checks Pangolin API)
- [x] Build-time version injection
- [x] Code split (httproute: 4 focused files)
- [x] Semver releases (CHANGELOG.md + versioned builds)
- [ ] Admission webhooks (documented)

---

## 📚 References

- Article: [Kubernetes Operators Explained by a Production Engineer](https://hackernoon.com/kubernetes-operators-explained-by-a-production-engineer)
- Implementation Docs: `IMPROVEMENTS.md`, `RUNBOOK.md`, `CLAUDE.md`
- Code Files: `pkg/controller/*.go`, `pkg/metrics/metrics.go`, `pkg/pangolin/errors.go`
