# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

A Kubernetes Gateway API controller that integrates with Pangolin for advanced traffic management and secure access control. The controller implements a 5-tier reconciliation model bridging Kubernetes Gateway API resources with Pangolin's access management platform via REST API.

## Essential Commands

### Development

```bash
# Run locally (requires environment variables)
export PANGOLIN_API_KEY=xxx PANGOLIN_ORG_ID=xxx
export PANGOLIN_BASE_URL=https://api.example.com/v1
export ENABLE_LEADER_ELECTION=false LOG_LEVEL=debug
make run

# Build binary
make build              # Outputs to bin/controller

# Run tests
make test               # Runs all unit tests with coverage
go test ./pkg/...       # Run specific package tests

# Lint and format
make fmt                # Format code
make vet                # Run go vet
make lint               # Run golangci-lint
```

### Docker & Deployment

```bash
# Build and deploy to kind
make docker-build IMG=pangolin-gateway-controller:latest
kind load docker-image pangolin-gateway-controller:latest
make deploy

# Install Gateway API CRDs (v1.4.1)
make install

# Deploy examples
make deploy-examples
```

## Architecture

### 5-Tier Reconciliation Model

1. **Gateway Reconciler** (`pkg/controller/gateway_controller.go`)
   - Controller name: `gateway-site` (unique identifier)
   - Creates Pangolin newt-type Site named `pgc-{gateway-name}`
   - Uses `PickSiteDefaults()` API to get subnet, exit node, and credentials
   - Stores site ID in Gateway label: `gateway.pangolin.net/site-id`
   - Creates Secret with newt credentials for VPN authentication
   - Finalizer ensures site deletion when Gateway is deleted

2. **Newt Reconciler** (`pkg/controller/newt_controller.go`)
   - Controller name: `gateway-newt` (unique identifier)
   - Automatically deploys WireGuard VPN instances for Gateways
   - Reads credentials from Secret: `{gateway-name}-newt-cred`
   - Creates Deployment + ClusterIP Service for WireGuard (ports 51820, 51821 UDP)
   - Newt endpoint auto-derived: `api.example.com` → `pangolin.example.com`

3. **HTTPRoute Reconciler** (`pkg/controller/httproute_controller.go`)
   - One Pangolin resource per unique hostname (shared across HTTPRoutes)
   - Resources named by full hostname (e.g., "test.dev0ps.me")
   - Path-based target ownership: Each HTTPRoute manages only its rule paths
   - Creates targets pointing to Service ClusterIP addresses
   - SSO configuration via annotation: `gateway.pangolin.net/disable-sso=true`

4. **GRPCRoute Reconciler** (`pkg/controller/grpcroute_controller.go`)
   - Handles TCP/UDP services
   - Protocol specified via annotation: `gateway.pangolin.net/protocol` (tcp/udp)
   - Creates Pangolin resources with `http:false`

5. **Pangolin Client** (`pkg/pangolin/client.go`)
   - REST API wrapper for Pangolin Integration API
   - Bearer token authentication
   - All responses wrapped in `{"data": {...}}` structure

### Key Design Patterns

#### Resource Idempotency

- Always check for existing resources by name before creating
- Pattern: `findExistingResourceBySubdomain()` searches via `ListResources()`
- Prevents duplicate creation and 409 conflicts

#### Path-Based Target Ownership

- Multiple HTTPRoutes can share the same Pangolin resource (same hostname)
- Each HTTPRoute manages only targets matching its rule paths
- Prevents deletion loops when routes share resources

#### Credential Preservation (CRITICAL)

```go
// CreateSite() does NOT return credentials - must preserve from PickSiteDefaults
defaults, _ := client.PickSiteDefaults(ctx)
site := &Site{NewtID: defaults.NewtID, Secret: defaults.NewtSecret}
createdSite, _ := client.CreateSite(ctx, site)
// CRITICAL: Preserve credentials from original defaults
createdSite.NewtID = site.NewtID
createdSite.Secret = site.Secret
```

#### Controller Name Uniqueness

Multiple controllers watching the same resource type MUST have unique names:

```go
ctrl.NewControllerManagedBy(mgr).
    For(&gatewayv1.Gateway{}).
    Named("gateway-site").  // Unique identifier
    Complete(r)
```

## API Integration

### Pangolin Integration API Endpoints

- **Integration API** (`api.example.com/v1`) - Bearer token auth, used by controller
- **UI API** (`pangolin.example.com/api`) - Session cookie auth, web interface

Key Integration API endpoints:

- `GET /org/{orgId}/pick-site-defaults` - Auto-allocate subnet, exit node, credentials
- `PUT /org/{orgId}/site` - Create site (doesn't return credentials!)
- `GET /org/{orgId}/sites` - List sites
- `DELETE /site/{siteId}` - Delete site
- `PUT /org/{orgId}/resource` - Create resource (returns resourceId)
- `PUT /resource/{resourceId}/target` - Create target with routing properties
- `DELETE /resource/{resourceId}` - Delete resource
- `DELETE /target/{targetId}` - Delete target

### API Limitations

- Self-hosted Integration API has limited endpoints vs official API
- `PATCH /resource/{resourceId}` NOT supported (only in UI API)
- SSO disable requires PATCH endpoint, so manual UI intervention needed
- Target schema uses IP addresses (ClusterIP), not DNS names

## Configuration

### Environment Variables

```bash
# Required
PANGOLIN_API_KEY=xxx
PANGOLIN_ORG_ID=xxx

# Optional (with defaults)
PANGOLIN_BASE_URL=https://api.pangolin.net/v1
NEWT_ENDPOINT=https://api.pangolin.net  # Auto-derived from BASE_URL
NEWT_IMAGE=docker.io/fosrl/newt:1.9.0
GATEWAY_CLASS_NAME=pangolin
WATCH_NAMESPACE=""  # Empty = all namespaces
ENABLE_LEADER_ELECTION=true
LOG_LEVEL=info
```

### Config File

Alternative to environment variables:

```bash
./bin/controller --config config/config.example.yaml
```

## Testing Strategy

### Local Testing with Kind

```bash
# 1. Create cluster and install CRDs
kind create cluster
make install

# 2. Build and load image
make docker-build IMG=pangolin-gateway-controller:latest
kind load docker-image pangolin-gateway-controller:latest

# 3. Set up credentials
kubectl create namespace pangolin-system
kubectl create secret generic pangolin-api-credentials \
  --from-literal=apiKey=$PANGOLIN_API_KEY \
  --from-literal=orgId=$PANGOLIN_ORG_ID \
  -n pangolin-system

# 4. Deploy controller
make deploy

# 5. Deploy test application
kubectl apply -f examples/full-test.yaml

# 6. Check newt VPN logs
kubectl logs -l app.kubernetes.io/name=newt --tail=50
# Expected: "Tunnel connection to server established successfully!"

# 7. Verify site online
curl -H "Authorization: Bearer $PANGOLIN_API_KEY" \
  https://api.example.com/v1/org/home/sites | jq '.data.sites[] | {name, online}'
```

### Common Test Scenarios

- **Path-based routing**: `examples/httproute.yaml`
- **Weighted traffic splitting**: `examples/canary.yaml`
- **TCP/UDP services**: `examples/grpcroute.yaml`
- **Full integration test**: `examples/full-test.yaml`

## Gateway API Version Requirements

- **Gateway API v1.4.1** (updated from v1.0.0)
- GRPCRoute moved to `v1` (was `v1alpha2`)
- Import path: `gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"`
- Use `metav1.Condition` for status, NOT `gatewayv1.Condition`

## Common Pitfalls

1. **Missing GatewayClass filter**: Always check `gateway.Spec.GatewayClassName == r.ControllerClass`
2. **Credential loss**: CreateSite doesn't return credentials - preserve from PickSiteDefaults
3. **Wrong endpoint for newt**: Newt connects to `pangolin.example.com`, not `api.example.com/v1`
4. **Finalizer deadlock**: Remove finalizer AFTER cleaning up Pangolin resources
5. **Service ClusterIP**: Use ClusterIP addresses in targets, not DNS names
6. **RBAC permissions**: Requires Secrets, Deployments, Services - not just Gateway API resources
7. **Controller name conflicts**: Use unique names via `.Named()` when multiple controllers watch same resource
8. **Shared resource deletion**: Only delete targets matching current HTTPRoute's paths
9. **Invalid hostnames**: Filter hostnames that don't match Pangolin domains

## Debugging

### Newt VPN Won't Connect

```bash
# Check credentials secret
kubectl get secret {gateway-name}-newt-cred -o yaml

# Check PANGOLIN_ENDPOINT (should be https://pangolin.example.com, not api.example.com/v1)
kubectl get secret {gateway-name}-newt-cred -o jsonpath='{.data.PANGOLIN_ENDPOINT}' | base64 -d

# Check newt logs
kubectl logs -l app.kubernetes.io/name=newt --tail=50
# Common errors: 401 (bad credentials), 405 (wrong endpoint)
```

### Site Not Created

```bash
# Check controller logs
kubectl logs -n pangolin-system -l app=pangolin-gateway-controller --tail=50

# Test API manually
curl -H "Authorization: Bearer $PANGOLIN_API_KEY" \
  https://api.example.com/v1/org/home/sites | jq
```

### Gateway Stuck in Non-Programmed State

```bash
# Check Gateway status conditions
kubectl describe gateway {name}

# Verify GatewayClass exists
kubectl get gatewayclass pangolin -o yaml
```

### Enable Debug Logging

```bash
# Set LOG_LEVEL=debug or use --zap-log-level=1 flag
export LOG_LEVEL=debug
make run

# In deployed controller
kubectl set env deployment/pangolin-gateway-controller LOG_LEVEL=debug -n pangolin-system
```

## Project Structure

```shell
.
├── cmd/controller/          # Main entry point
├── pkg/
│   ├── config/             # Configuration loading and validation
│   ├── controller/         # Reconcilers (gateway, newt, httproute, grpcroute)
│   └── pangolin/           # Pangolin API client
├── config/                 # Kubernetes manifests (RBAC, deployment)
├── examples/               # Example Gateway/HTTPRoute resources
├── k8s/
│   ├── app/               # Kubernetes app manifests
│   └── chart/             # Helm chart
└── docs/                  # Documentation
```

## Logging Strategy

- **Info level** (default): Reconciliation events, errors, major state changes
- **V(1) Debug level**: Verbose details for troubleshooting
  - Use `log.V(1).Info()` for debug-level logs
  - Set via `LOG_LEVEL=debug` or `--zap-log-level=1`
  - Includes domain matching, target drift detection, resource existence checks
