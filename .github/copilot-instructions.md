# Pangolin Gateway Controller - AI Agent Instructions

## Project Architecture

This is a **Kubernetes Gateway API controller** written in Go that bridges Kubernetes Gateway/HTTPRoute resources with the Pangolin platform's access management API.

### Core Components (5-tier reconciliation model)

1. **Pangolin Client** (`pkg/pangolin/client.go`): REST API wrapper for Pangolin Integration API
   - Manages Sites and Resources via Integration API (self-hosted Pangolin)
   - Uses Bearer token authentication, requires `PANGOLIN_API_KEY` and `PANGOLIN_ORG_ID`
   - Key methods: `CreateSite()`, `ListSites()`, `DeleteSite()`, `PickSiteDefaults()`, `ListResources()`, `CreateResource()`
   - **CRITICAL**: `CreateSite()` does NOT return credentials - must preserve from `PickSiteDefaults()`
   - Response format: All API responses wrapped in `{"data": {...}}` structure

2. **Gateway Reconciler** (`pkg/controller/gateway_controller.go`):
   - Watches `Gateway` resources with `gatewayClassName: pangolin`
   - Creates Pangolin **newt-type Site** named `pgc-{gateway-name}` (not namespace-based)
   - Uses `PickSiteDefaults()` API to get subnet allocation, exit node, and credentials
   - Stores site ID in Gateway label: `gateway.pangolin.net/site-id`
   - Uses finalizers (`gateway.pangolin.net/finalizer`) for cleanup - **DELETES SITE ON GATEWAY DELETION**
   - Creates Secret with newt credentials for VPN authentication
   - Periodic reconciliation every 5 minutes to verify site still exists
   - Auto-recreates site if deleted from Pangolin
   - **Controller name**: `gateway-site` (unique identifier to avoid conflicts with Newt controller)

3. **Newt Reconciler** (`pkg/controller/newt_controller.go`):
   - Watches Gateway resources and automatically deploys newt VPN instances
   - Reads credentials from Secret: `{gateway-name}-newt-cred`
   - Deploys newt Deployment with WireGuard container (configurable via `NEWT_IMAGE`, default: fosrl/newt:1.9.0)
   - Creates ClusterIP Service for WireGuard ports (51820, 51821 UDP)
   - All resources use OwnerReferences to Gateway for automatic cleanup
   - **Newt endpoint auto-derived**: If API is `api.example.com`, newt connects to `pangolin.example.com`
   - **Controller name**: `gateway-newt` (unique identifier)

4. **HTTPRoute Reconciler** (`pkg/controller/httproute_controller.go`):
   - Watches `HTTPRoute` resources
   - **Resource idempotency**: Checks for existing resources via `ListResources()` before creating (matches by subdomain)
   - Creates Pangolin resources via Integration API for HTTP traffic (`PUT /org/{orgId}/resource`)
   - Creates targets pointing to Service ClusterIP addresses (`PUT /resource/{resourceId}/target`)
   - **One target per HTTPRoute rule** (not per backend) with routing properties: path, pathMatchType, priority
   - Queries organization domains to match hostnames dynamically
   - Periodic reconciliation every 5 minutes to verify resource still exists
   - Auto-recreates resource if deleted from Pangolin
   - SSO configuration: Controlled via annotation `gateway.pangolin.net/disable-sso=true` (default: false/enabled)
   - Label storage: Stores `gateway.pangolin.net/resource-id` in HTTPRoute labels for tracking

5. **GRPCRoute Reconciler** (`pkg/controller/grpcroute_controller.go`):
   - Watches `GRPCRoute` resources for TCP/UDP services
   - Creates Pangolin resources with `http:false` and `protocol:"tcp"|"udp"`
   - Uses annotation `gateway.pangolin.net/protocol` to specify tcp or udp (default: tcp)
   - Same target/resource management as HTTPRoute
   - Periodic reconciliation every 5 minutes
   - Auto-recreates resource if deleted from Pangolin

### Data Flow Pattern

```
K8s Gateway → Pangolin Newt Site (via PickSiteDefaults API)
               ↓
          Site credentials (NEWT_ID, NEWT_SECRET, subnet, exitNode)
               ↓
          Secret: {gateway-name}-newt-cred (PANGOLIN_ENDPOINT, NEWT_ID, NEWT_SECRET)
               ↓
          Newt Deployment (WireGuard VPN) + ClusterIP Service
               ↓
          VPN tunnel established to Pangolin exit node
```

**Endpoint Configuration:**
- `PANGOLIN_BASE_URL` (e.g., `https://api.example.com/v1`) - Integration API for controller
- `NEWT_ENDPOINT` (auto-derived to `https://pangolin.example.com`) - VPN authentication endpoint
- Logic: Strip `api.` prefix, add `pangolin.` prefix, remove `/v1` path

## Development Workflow

### Essential Commands
```bash
# Run locally (disables leader election for dev)
export PANGOLIN_API_KEY=xxx PANGOLIN_ORG_ID=xxx
export PANGOLIN_BASE_URL=https://api.example.com/v1
export ENABLE_LEADER_ELECTION=false LOG_LEVEL=debug
make run

# Build & test
make build          # Builds to bin/controller
make test           # Run unit tests
make docker-build   # Build container image (IMG=name:tag)

# Deploy to kind cluster
make install        # Install Gateway API CRDs
kind load docker-image pangolin-gateway-controller:latest
make deploy IMG=pangolin-gateway-controller:latest
```

### Testing Pattern (Kind + Self-Hosted Pangolin)
1. Start kind cluster: `kind create cluster`
2. Deploy controller: `make docker-build && kind load docker-image ... && make deploy`
3. Deploy nginx test app: `kubectl apply -f deploy-nginx-app.yaml`
4. Create Gateway: `kubectl apply -f examples/gateway.yaml`
5. Check newt VPN logs: `kubectl logs -l app.kubernetes.io/name=newt`
   - Success: "Tunnel connection to server established successfully!"
   - Common errors: 401 (bad credentials), 405 (wrong endpoint)
6. Verify site online: `curl -H "Authorization: Bearer $PANGO_KEY" https://api.example.com/v1/org/home/sites | jq '.data.sites[] | {name, online}'`

## Project-Specific Conventions

### Gateway API Version Requirements
- **Gateway API v1.4.1** (updated from v1.0.0)
- GRPCRoute now in `v1` (was `v1alpha2` in earlier versions)
- HTTPRoute and Gateway remain in `v1`
- **CRITICAL**: When updating Gateway API versions, check if alpha resources moved to stable
- Import paths: Use `gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"` for all v1 resources

### Controller Name Uniqueness
- **Multiple controllers watching same resource type MUST have unique names**
- Gateway controller: `.Named("gateway-site")` - manages Pangolin site creation
- Newt controller: `.Named("gateway-newt")` - manages VPN deployment
- Prevents controller-runtime conflicts when multiple reconcilers watch Gateway resources
- Pattern: `ctrl.NewControllerManagedBy(mgr).For(&Resource{}).Named("unique-name").Complete(r)`

### Resource Idempotency Pattern
```go
// HTTPRoute controller checks for existing resources BEFORE creating
func (r *HTTPRouteReconciler) findExistingResource(ctx, route, log) (string, error) {
    // 1. Calculate expected subdomain from HTTPRoute hostname
    // 2. Call ListResources() to get all organization resources
    // 3. Match by subdomain field
    // 4. Return resourceID if found, empty string if not
}

// In reconcileHTTPRoute:
if resourceID == "" {
    existingResourceID, _ := r.findExistingResource(ctx, route, log)
    if existingResourceID != "" {
        // Use existing resource, update label
    } else {
        // Create new resource
    }
}
```
- **Why**: Prevents "Resource with that domain already exists" (409 errors)
- **Pattern**: Check existence by business key (subdomain), not just K8s label
- Resources tracked via label `gateway.pangolin.net/resource-id`

### Site Naming & Deletion
- Sites named `pgc-{gateway-name}` (not namespace-based)
- Finalizer ensures site deletion: `DeleteSite(siteID)` called before Gateway removal
- Site ID stored in label, parsed with `strconv.Atoi()`

### Credential Preservation (CRITICAL BUG FIX)
```go
// WRONG - CreateSite doesn't return credentials
createdSite, _ := client.CreateSite(ctx, site)
secret.Data["NEWT_ID"] = createdSite.NewtID // Empty!

// CORRECT - Preserve from PickSiteDefaults
defaults, _ := client.PickSiteDefaults(ctx)
site := &Site{NewtID: defaults.NewtID, Secret: defaults.NewtSecret}
createdSite, _ := client.CreateSite(ctx, site)
createdSite.NewtID = site.NewtID  // Preserve!
createdSite.Secret = site.Secret
```

### Endpoint Auto-Derivation
```go
// In config.ApplyDefaults()
if c.Controller.NewtEndpoint == "" {
    parsed, _ := url.Parse(c.Pangolin.BaseURL)
    host := parsed.Hostname()
    host = strings.TrimPrefix(host, "api.") // Remove api. prefix
    c.Controller.NewtEndpoint = fmt.Sprintf("%s://pangolin.%s", parsed.Scheme, host)
}
```

### Status Updates Pattern
- Update Gateway status with conditions: `Programmed`, `Accepted`
- HTTPRoute status updates per parent: `RouteParentStatus` with `ControllerName: pangol.in/gateway-controller`
- Always set `ObservedGeneration` to detect stale status
- **IMPORTANT**: Use `metav1.Condition`, NOT `gatewayv1.Condition` (Gateway API v1.0.0+)

### Reconciliation Idempotency
- Check if Pangolin resource exists before creating:
  1. If no label: Call `ListResources()` to find by subdomain
  2. If has label: Call `ListTargetsRaw()` to verify resource exists
- For updates: delete old Rules/Targets then recreate (Pangolin API doesn't have PATCH)
- Finalizers ensure Pangolin resources are cleaned up before K8s resource deletion
- Target idempotency: Check `ip:port:siteId:path` before creating new target

### Error Handling Strategy
- Transient errors: requeue with `ctrl.Result{RequeueAfter: 30 * time.Second}`
- Permanent errors: update status condition with error message, don't requeue
- API errors: log but continue, will retry on next reconciliation

## Configuration & Secrets

- **Environment Variables** (preferred for deployments):
  ```bash
  PANGOLIN_API_KEY, PANGOLIN_ORG_ID (required)
  PANGOLIN_BASE_URL=https://api.pangolin.net/v1 (Integration API)
  NEWT_ENDPOINT=https://api.pangolin.net (optional, auto-derived from BASE_URL)
  NEWT_IMAGE=docker.io/fosrl/newt:1.9.0
  GATEWAY_CLASS_NAME=pangolin
  WATCH_NAMESPACE="" (empty = all namespaces)
  LOG_LEVEL=info|debug
  ```

- **Config File** (for local dev): `--config config/config.example.yaml`
- Credentials stored in Secret: `pangolin-api-credentials` in `pangolin-system` namespace
- Newt credentials per Gateway: `{gateway-name}-newt-cred` in same namespace

## Integration Points

### Kubernetes APIs
- Uses controller-runtime v0.17.0 with Watch/Cache pattern
- Gateway API v1.4.1 (GRPCRoute in `v1`, HTTPRoute/Gateway in `v1`)
- **CRITICAL**: Use `metav1.Condition` for status, NOT `gatewayv1.Condition`
- RBAC requires: Gateway API resources, Secrets, Deployments, Services (full CRUD)
- Leader election via `coordination.k8s.io/leases` in pangolin-system namespace

### Pangolin Integration API Endpoints
- `GET /org/{orgId}/pick-site-defaults` - Auto-allocate subnet, exit node, credentials
- `PUT /org/{orgId}/site` - Create site (doesn't return credentials!)
- `GET /org/{orgId}/sites` - List sites (note: plural)
- `DELETE /site/{siteId}` - Delete site (no org in path)
- `GET /org/{orgId}/resources` - List all resources (for idempotency checking)
- `PUT /org/{orgId}/resource` - Create resource (returns resourceId)
- `GET /resource/{resourceId}/targets` - List targets (verify resource exists)
- `PUT /resource/{resourceId}/target` - Create target with routing properties
- `DELETE /resource/{resourceId}` - Delete resource
- `DELETE /target/{targetId}` - Delete target
- All use JSON payloads with `{"data": {...}}` wrapper, Bearer token in `Authorization` header

**Integration API Limitations:**
- Official API docs show full support for SiteResource/Rules/Targets CRUD operations
- Self-hosted Integration API versions (e.g., example.com) may have limited endpoints
- **TWO SEPARATE APIS**: Integration API (`api.example.com`) vs UI API (`pangolin.example.com/api`)
  - Integration API: Bearer token auth, used by controller
  - UI API: Session cookie auth, used by web interface
  - Different capabilities and endpoints!
- **Endpoint differences** (Integration API):
  - ❌ `PUT /org/{orgId}/site-resource` - Returns "Cannot PUT"
  - ✅ `PUT /org/{orgId}/resource` - Works for creating resources
  - ✅ `PUT /resource/{resourceId}/target` - Works for creating targets
  - ✅ `DELETE /resource/{resourceId}` - Works for deleting resources
  - ✅ `DELETE /target/{targetId}` - Works for deleting targets
  - ✅ `POST /resource/{resourceId}/roles` - Sets allowed roles (empty array = no role restrictions)
  - ❌ `PATCH /resource/{resourceId}` - **Not supported in Integration API** (only in UI API)
- **Resource schema**: `{name, subdomain, http:boolean, protocol:"tcp"|"udp", domainId:string, stickySession:boolean}`
- **SSO Limitation** (Critical Discovery):
  - **Root cause**: SSO disable requires TWO API calls:
    1. `POST /resource/{resourceId}/roles` with `{"roleIds":[]}` ✅ Available in Integration API
    2. `PATCH /resource/{resourceId}` with `{"sso":false}` ❌ **Only in UI API**
  - Integration API lacks PATCH endpoint, so controller can only do step 1
  - Result: POST /roles succeeds, but sso remains true without the PATCH
  - **Workaround**: Manually disable SSO in Pangolin UI after resource creation
  - Resources with sso:true require authentication (returns 503/auth required)
  - Use annotation `gateway.pangolin.net/disable-sso=false` to skip SSO disable attempt
- **Target schema**: `{siteId:int, ip:string, port:int, enabled:boolean, ...health check fields}`
  - Use ClusterIP or Pod IP, not DNS names (newt may not resolve cluster DNS)
- Cloud API (api.pangolin.net) may have full CRUD support
- Client code includes wrapped response handling (`{"data": {...}}`) for all endpoints

## Common Pitfalls

1. **Missing GatewayClass filter**: Always check `gateway.Spec.GatewayClassName == r.ControllerClass`
2. **Credential loss**: CreateSite doesn't return credentials - preserve from PickSiteDefaults response
3. **Wrong endpoint for newt**: Newt connects to `pangolin.example.com`, not `api.example.com/v1`
4. **Finalizer deadlock**: Remove finalizer AFTER cleaning up Pangolin resources, not before
5. **Service FQDN**: Always use cluster DNS format: `{service}.{namespace}.svc.cluster.local`
6. **RBAC permissions**: Need Secrets, Deployments, Services - not just Gateway API resources
7. **Newt container capabilities**: Requires `NET_ADMIN` for WireGuard tunnel creation
8. **Resource existence**: Always check `ListResources()` before creating to avoid 409 conflicts
9. **Controller name conflicts**: Multiple controllers watching same resource need unique names via `.Named()`
10. **Port mismatches**: Container images have default ports (nginx:80, not 8080) - must match service targetPort

## Key Files Reference
- Entry point: `cmd/controller/main.go` (controller-runtime setup with all 3 reconcilers)
- Constants: `pkg/controller/gateway_controller.go:23-30` (finalizer names, labels)
- API types: `pkg/pangolin/client.go:36-140` (Site, SiteDefaults structs)
- Config logic: `pkg/config/config.go:145-165` (newt endpoint auto-derivation)
- Newt deployment: `pkg/controller/newt_controller.go:180-280` (Secret, Deployment, Service builders)
- RBAC: `config/rbac.yaml` (includes Secrets, Deployments, Services permissions)

## Debugging Checklist

1. **Newt VPN won't connect?**
   - Check Secret has all 3 values: `kubectl get secret {gateway}-newt-cred -o yaml`
   - Check PANGOLIN_ENDPOINT value: Should be `https://pangolin.example.com`, not `https://api.example.com/v1`
   - Check newt logs: `kubectl logs -l app.kubernetes.io/name=newt --tail=50`
   - Common errors: 401 (bad credentials), 405 (wrong endpoint), 404 (DNS issue)

2. **Site not created?**
   - Check controller logs: `kubectl logs -n pangolin-system -l app=pangolin-gateway-controller`
   - Verify API credentials: `echo $PANGOLIN_API_KEY | cut -c1-20` (should not be empty)
   - Test API manually: `curl -H "Authorization: Bearer $KEY" https://api.example.com/v1/org/home/sites`

3. **Gateway stuck in non-Programmed state?**
   - Check Gateway status: `kubectl describe gateway {name}`
   - Look for condition messages with error details
   - Verify GatewayClass exists: `kubectl get gatewayclass pangolin`
