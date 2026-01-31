# Architecture

## Overview

The Pangolin Gateway Controller implements the Kubernetes Gateway API and integrates with Pangolin's access management platform to provide secure, scalable ingress traffic management.

## Components

### Controller Manager

The main process that runs the Gateway and HTTPRoute reconcilers. It uses the controller-runtime framework for Kubernetes resource management.

```text
┌─────────────────────────────────────────┐
│     Controller Manager                  │
│                                         │
│  ┌────────────────────────────────────┐ │
│  │  Gateway Reconciler                │ │
│  │  - Watch Gateway resources         │ │
│  │  - Create Pangolin sites           │ │
│  │  - Create Pangolin resources       │ │
│  └────────────────────────────────────┘ │
│                                         │
│  ┌────────────────────────────────────┐ │
│  │  HTTPRoute Reconciler              │ │
│  │  - Watch HTTPRoute resources       │ │
│  │  - Create routing rules            │ │
│  │  - Configure backend targets       │ │
│  └────────────────────────────────────┘ │
│                                         │
└─────────────────────────────────────────┘
           │                    │
           │                    │
           ▼                    ▼
   ┌──────────────┐    ┌──────────────┐
   │  Kubernetes  │    │   Pangolin   │
   │     API      │    │     API      │
   └──────────────┘    └──────────────┘
```

### Pangolin Client

A Go client library that wraps the Pangolin Integration API:

- Authentication with Bearer tokens
- CRUD operations for sites, resources, targets, and rules
- Error handling and retries
- JSON marshaling/unmarshaling

### Resource Mapping

| Kubernetes Resource | Pangolin Resource | Description |
|---------------------|-------------------|-------------|
| Gateway | Site + SiteResource | Infrastructure endpoint |
| HTTPRoute | Rules + Targets | Routing configuration |
| Service | Target | Backend endpoint |

## Reconciliation Flow

### Gateway Reconciliation

1. **Watch**: Controller watches Gateway resources
2. **Validate**: Check GatewayClass matches controller name
3. **Site Creation**: Create or find Pangolin site for namespace
4. **Resource Creation**: Create SiteResource in Pangolin
5. **Status Update**: Update Gateway status conditions
6. **Label Update**: Add resource ID labels to Gateway

### HTTPRoute Reconciliation

1. **Watch**: Controller watches HTTPRoute resources
2. **Parent Lookup**: Find parent Gateway and get resource ID
3. **Target Sync**: Create/update backend targets in Pangolin
4. **Rule Sync**: Create/update routing rules in Pangolin
5. **Status Update**: Update HTTPRoute parent status

## Data Flow

```text
┌─────────────────────────────────────────────────────────┐
│ User creates Gateway and HTTPRoute                      │
└─────────────────────────────────────────────────────────┘
                          │
                          ▼
┌─────────────────────────────────────────────────────────┐
│ Controller receives event via Kubernetes watch          │
└─────────────────────────────────────────────────────────┘
                          │
                          ▼
┌─────────────────────────────────────────────────────────┐
│ Gateway Reconciler:                                     │
│ 1. Create Pangolin site (k8s-namespace)                 │
│ 2. Create SiteResource with listeners config            │
│ 3. Store resource ID in Gateway labels                  │
└─────────────────────────────────────────────────────────┘
                          │
                          ▼
┌─────────────────────────────────────────────────────────┐
│ HTTPRoute Reconciler:                                   │
│ 1. Get resource ID from parent Gateway                  │
│ 2. Create Targets for each backend service              │
│ 3. Create Rules with conditions and actions             │
└─────────────────────────────────────────────────────────┘
                          │
                          ▼
┌─────────────────────────────────────────────────────────┐
│ Pangolin provisions infrastructure and routing          │
└─────────────────────────────────────────────────────────┘
                          │
                          ▼
┌─────────────────────────────────────────────────────────┐
│ Traffic flows through Pangolin to Kubernetes services   │
└─────────────────────────────────────────────────────────┘
```

## Error Handling

- **Transient Errors**: Requeue with exponential backoff
- **Permanent Errors**: Update status conditions with error message
- **API Errors**: Log and update status, retry on next reconciliation
- **Finalizers**: Ensure cleanup even if resources are deleted

## High Availability

- **Leader Election**: Only one active controller at a time
- **Multiple Replicas**: Standby replicas ready for failover
- **Shared State**: All state in Kubernetes and Pangolin APIs
- **Stateless Design**: No local state, can restart anytime

## Security

- **RBAC**: Minimal required permissions
- **Secrets**: API credentials stored in Kubernetes Secrets
- **TLS**: HTTPS for all Pangolin API calls
- **Authentication**: Bearer token authentication
- **Pod Security**: Non-root, read-only filesystem, dropped capabilities

## Performance Considerations

- **Watch Filtering**: Only watch relevant resources
- **Batch Operations**: Group Pangolin API calls when possible
- **Caching**: Controller-runtime caches Kubernetes resources
- **Rate Limiting**: Built-in client-side rate limiting
- **Concurrent Reconciliation**: Multiple workers per controller
