# Architecture

## Overview

The Pangolin Gateway Controller implements the Kubernetes Gateway API and integrates with Pangolin's access management platform to provide secure, scalable ingress traffic management with automatic WireGuard VPN provisioning.

## Components

### Controller Manager

The main process that runs all reconcilers. It uses the controller-runtime framework for Kubernetes resource management.

```text
┌───────────────────────────────────────────────────────┐
│                  Controller Manager                   │
│                                                       │
│  ┌──────────────────────────────────────────────────┐ │
│  │  GatewayClass Reconciler                         │ │
│  │  - Watch GatewayClass resources                  │ │
│  │  - Set Accepted condition                        │ │
│  └──────────────────────────────────────────────────┘ │
│                                                       │
│  ┌──────────────────────────────────────────────────┐ │
│  │  Gateway Reconciler  (controller: gateway-site)  │ │
│  │  - Watch Gateway resources                       │ │
│  │  - Create Pangolin newt-type sites               │ │
│  │  - Manage credentials Secrets                    │ │
│  │  - Update Gateway status conditions              │ │
│  └──────────────────────────────────────────────────┘ │
│                                                       │
│  ┌──────────────────────────────────────────────────┐ │
│  │  Newt Reconciler     (controller: gateway-newt)  │ │
│  │  - Watch Gateway resources                       │ │
│  │  - Deploy WireGuard VPN pods (fosrl/newt)        │ │
│  │  - Create ClusterIP Service for VPN ports        │ │
│  └──────────────────────────────────────────────────┘ │
│                                                       │
│  ┌──────────────────────────────────────────────────┐ │
│  │  HTTPRoute Reconciler                            │ │
│  │  - Watch HTTPRoute resources                     │ │
│  │  - Create Pangolin resources and routing rules   │ │
│  │  - Configure backend targets                     │ │
│  │  - Detect and correct configuration drift        │ │
│  └──────────────────────────────────────────────────┘ │
│                                                       │
│  ┌──────────────────────────────────────────────────┐ │
│  │  GRPCRoute Reconciler                            │ │
│  │  - Watch GRPCRoute resources                     │ │
│  │  - Create TCP/UDP Pangolin resources             │ │
│  └──────────────────────────────────────────────────┘ │
│                                                       │
└───────────────────────────────────────────────────────┘
           │                    │
           ▼                    ▼
   ┌──────────────┐    ┌──────────────┐
   │  Kubernetes  │    │   Pangolin   │
   │     API      │    │     API      │
   └──────────────┘    └──────────────┘
```

### Pangolin Client

A Go client library wrapping the Pangolin Integration API:

- Bearer token authentication
- CRUD operations for sites, resources, targets, and rules
- Typed error handling via `PangolinAPIError` with `IsRetryable()`, `IsNotFound()`, `IsConflict()`
- Circuit breaker (opens after 5 consecutive retryable failures, resets after 30s)
- Pagination support for list endpoints

### Resource Mapping

| Kubernetes Resource | Pangolin Resource       | Description                       |
| ------------------- | ----------------------- | --------------------------------- |
| Gateway             | Site (newt-type)        | Pangolin access site for namespace |
| Gateway             | Deployment + Service    | WireGuard VPN pod via newt image  |
| Gateway             | Secret                  | NEWT_ID / NEWT_SECRET credentials |
| HTTPRoute           | SiteResource + Rules    | HTTP routing configuration        |
| GRPCRoute           | SiteResource (TCP/UDP)  | Layer-4 routing configuration     |
| Service             | Target                  | Backend endpoint with ClusterIP   |

## Reconciliation Flow

### Gateway Reconciliation

1. **Watch**: Controller watches Gateway resources
2. **Validate**: Check GatewayClass name matches `pangolin`
3. **Site Creation**: Create or find Pangolin newt-type site for the namespace (`pgc-{gateway-name}`)
4. **Credential Management**: Create or preserve existing `{gateway}-newt-cred` Secret
5. **Status Update**: Update Gateway status conditions (`Accepted`, `Programmed`) with `observedGeneration`
6. **Cleanup**: On deletion, finalizer removes the Pangolin site

### Newt VPN Reconciliation

1. **Watch**: Controller watches the same Gateway resources
2. **Endpoint Derivation**: Auto-derive `NEWT_ENDPOINT` from `PANGOLIN_BASE_URL` (strips `api.` prefix and `/v1` path)
3. **Deployment**: Create WireGuard VPN Deployment with `fosrl/newt` image
4. **Service**: Create ClusterIP Service exposing UDP ports 51820/51821
5. **Cleanup**: OwnerReferences on all child resources enable automatic garbage collection

### HTTPRoute Reconciliation

1. **Watch**: Controller watches HTTPRoute resources
2. **Parent Lookup**: Find parent Gateway labels to get Pangolin resource ID
3. **Idempotency Check**: Use `ListResources()` to find existing resource by subdomain before creating
4. **Target Sync**: Create, update, or delete backend targets; detect drift (path, priority, method)
5. **Rule Sync**: Create routing rules based on path, header, and hostname matches
6. **Status Update**: Update HTTPRoute parent status per parent Gateway

## Data Flow

```text
┌──────────────────────────────────────────────────────────┐
│ User creates Gateway and HTTPRoute                       │
└──────────────────────────────────────────────────────────┘
                          │
                          ▼
┌──────────────────────────────────────────────────────────┐
│ Controller receives event via Kubernetes watch           │
└──────────────────────────────────────────────────────────┘
                          │
                    ┌─────┴─────┐
                    ▼           ▼
┌────────────────────────┐  ┌───────────────────────────┐
│ Gateway Reconciler:    │  │ Newt Reconciler:           │
│ 1. Create Pangolin     │  │ 1. Create WireGuard pod    │
│    site (pgc-name)     │  │ 2. Create ClusterIP svc    │
│ 2. Create/preserve     │  │ 3. Derive NEWT_ENDPOINT    │
│    credentials Secret  │  │    from PANGOLIN_BASE_URL  │
│ 3. Update Gateway      │  └───────────────────────────┘
│    status conditions   │
└────────────────────────┘
                          │
                          ▼
┌──────────────────────────────────────────────────────────┐
│ HTTPRoute Reconciler:                                    │
│ 1. Get resource ID from parent Gateway labels            │
│ 2. Create Targets for each backend service (ClusterIP)   │
│ 3. Create Rules with path/header/hostname conditions     │
│ 4. Detect and correct configuration drift                │
└──────────────────────────────────────────────────────────┘
                          │
                          ▼
┌──────────────────────────────────────────────────────────┐
│ Pangolin provisions infrastructure and routing           │
└──────────────────────────────────────────────────────────┘
                          │
                          ▼
┌──────────────────────────────────────────────────────────┐
│ Traffic flows through Pangolin tunnel to backend pods    │
└──────────────────────────────────────────────────────────┘
```

## Error Handling

- **Transient Errors**: Requeue with exponential backoff (token bucket + exponential rate limiter)
- **Permanent Errors**: Update status conditions with error message and reason
- **Circuit Breaker**: Opens after 5 consecutive retryable API failures; resets after 30s
- **Finalizers**: Ensure Pangolin site cleanup even when resources are deleted concurrently
- **Conflict Retry**: `retry.RetryOnConflict()` with re-fetch for all status updates

## High Availability

- **Leader Election**: Only one active controller at a time; standby replicas ready for failover
- **Multiple Replicas**: Recommended 2 replicas with `terminationGracePeriodSeconds: 30`
- **Shared State**: All state in Kubernetes and Pangolin APIs; no local state
- **Stateless Design**: Controller can restart at any time and reconcile back to desired state

## Security

- **RBAC**: Minimal required permissions (least privilege, generated by kubebuilder)
- **Secrets**: API credentials stored in Kubernetes Secrets, never logged
- **TLS**: HTTPS for all Pangolin Integration API calls
- **Authentication**: Bearer token; `Ping()` omits the `Authorization` header to avoid credential leakage in proxy logs
- **Pod Security**: Non-root user (65532), read-only root filesystem, all capabilities dropped

## Performance

- **Field Indexing**: `O(1)` lookups for Gateway-by-class and Route-by-parent-Gateway
- **Concurrent Reconciliation**: 3–5 workers per controller
- **Controller-Runtime Cache**: Kubernetes resources cached locally, reducing API server load
- **Rate Limiting**: Client-side token-bucket + exponential backoff rate limiter
- **Pagination**: `limit`/`offset` pagination for Pangolin list endpoints
