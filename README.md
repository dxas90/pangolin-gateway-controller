# Pangolin Gateway Controller

A Kubernetes Gateway API controller that integrates with [Pangolin](https://pangolin.net) for advanced traffic management and secure access control.

## Overview

This controller implements the Kubernetes [Gateway API](https://gateway-api.sigs.k8s.io/) specification and manages infrastructure provisioning through the Pangolin platform. It enables declarative configuration of ingress traffic routing, load balancing, and secure access policies.

### Features

- ✅ Full Kubernetes Gateway API v1.4.1 support
- ✅ HTTPRoute and GRPCRoute resources (v1)
- ✅ Automatic Pangolin site and resource management
- ✅ Dynamic routing rule configuration with drift detection
- ✅ Backend service discovery via ClusterIP
- ✅ Header-based routing
- ✅ Path-based routing (exact, prefix, regex)
- ✅ Weighted traffic splitting (priority-based)
- ✅ Resource idempotency (prevents duplicate creation)
- ✅ Configuration drift detection and auto-correction
- ✅ Automatic WireGuard VPN deployment (newt)
- ✅ Leader election for high availability
- ✅ Prometheus metrics

## Architecture

The controller implements a **5-tier reconciliation model** that bridges Kubernetes Gateway API resources with Pangolin's access management platform:

```text
Kubernetes Resources          Pangolin Resources
-------------------          ------------------
Gateway            ──────►   Newt Site (VPN)
       │
       └──────────────────►   Newt Deployment (WireGuard)
       │
       └──────────────────►   Secret (credentials)

HTTPRoute          ──────►   Resource + Targets (with routing rules)
GRPCRoute          ──────►   Resource + Targets (TCP/UDP)
```

### Key Components

1. **Gateway Reconciler** (`gateway-site`) - Creates Pangolin newt-type sites, manages credentials
2. **Newt Reconciler** (`gateway-newt`) - Deploys WireGuard VPN pods, establishes secure tunnels
3. **HTTPRoute Reconciler** - Manages HTTP resources with path-based routing and priority
4. **GRPCRoute Reconciler** - Handles TCP/UDP services
5. **Pangolin Client** - Integration API wrapper with idempotency and drift detection

### Components

- **Gateway Controller**: Manages Gateway resources and creates Pangolin site resources
- **HTTPRoute Controller**: Configures HTTP routing rules and backend targets in Pangolin
- **Pangolin Client**: Go client library for Pangolin Integration API

## Prerequisites

- Kubernetes 1.25+
- Gateway API CRDs v1.4.1+ (GRPCRoute in v1)
- Pangolin account with Integration API access
- Go 1.24+ (for development)
- Docker (for building images)
- Kind or similar Kubernetes cluster (for testing)

## Installation

### 1. Install Gateway API CRDs

```bash
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.4.1/standard-install.yaml
```

### 2. Create Pangolin API Credentials

1. Log in to your Pangolin admin panel
2. Navigate to **Organization → API Keys**
3. Click **Create API Key**
4. Select required permissions:
   - Site management
   - Resource management
   - Rule management
   - Target management
5. Copy the generated API key and your organization ID

### 3. Deploy the Controller

```bash
# Clone the repository
git clone https://github.com/dxas90/pangolin-gateway-controller
cd pangolin-gateway-controller

# Update the credentials in config/deployment.yaml
kubectl create secret generic pangolin-api-credentials \
  --from-literal=apiKey=YOUR_API_KEY \
  --from-literal=orgId=YOUR_ORG_ID \
  -n pangolin-system

# Install RBAC and controller
kubectl apply -f config/rbac.yaml
kubectl apply -f config/gatewayclass.yaml
kubectl apply -f config/deployment.yaml
```

### 4. Verify Installation

```bash
# Check controller logs
kubectl logs -n pangolin-system -l app=pangolin-gateway-controller

# Verify GatewayClass
kubectl get gatewayclass pangolin
```

## Quick Start

### 1. Create a Gateway

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: example-gateway
  namespace: default
spec:
  gatewayClassName: pangolin
  listeners:
    - name: http
      protocol: HTTP
      port: 80
      allowedRoutes:
        namespaces:
          from: Same
```

```bash
kubectl apply -f gateway.yaml
```

### 2. Create an HTTPRoute

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: example-route
  namespace: default
spec:
  parentRefs:
    - name: example-gateway
  hostnames:
    - "www.example.com"
  rules:
    - matches:
        - path:
            type: PathPrefix
            value: /api
      backendRefs:
        - name: api-service
          port: 8080
    - matches:
        - path:
            type: PathPrefix
            value: /
      backendRefs:
        - name: web-service
          port: 80
```

```bash
kubectl apply -f httproute.yaml
```

### 3. Verify Resources

```bash
# Check Gateway status
kubectl get gateway example-gateway -o yaml

# Check HTTPRoute status
kubectl get httproute example-route -o yaml

# Check in Pangolin admin panel
# Navigate to your organization → Sites → k8s-default
```

## Configuration

### Environment Variables

The controller can be configured via environment variables:

| Variable | Description | Default |
| -------- | ----------- | ------- |
| `PANGOLIN_API_KEY` | Pangolin API key (required) | - |
| `PANGOLIN_ORG_ID` | Pangolin organization ID (required) | - |
| `PANGOLIN_BASE_URL` | Pangolin API base URL | `https://api.pangolin.net/v1` |
| `GATEWAY_CLASS_NAME` | GatewayClass name to manage | `pangolin` |
| `WATCH_NAMESPACE` | Namespace to watch (empty for all) | `""` |
| `METRICS_BIND_ADDRESS` | Metrics server address | `:8080` |
| `HEALTH_PROBE_BIND_ADDRESS` | Health probe address | `:8081` |
| `ENABLE_LEADER_ELECTION` | Enable leader election | `true` |
| `LOG_LEVEL` | Log level (debug, info, warn, error) | `info` |

### Configuration File

Alternatively, use a configuration file:

```bash
./controller --config config.yaml
```

See [config.example.yaml](config/config.example.yaml) for reference.

## Examples

### Path-Based Routing

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: path-routing
spec:
  parentRefs:
    - name: example-gateway
  rules:
    - matches:
        - path:
            type: PathPrefix
            value: /api/v1
      backendRefs:
        - name: api-v1-service
          port: 8080
    - matches:
        - path:
            type: PathPrefix
            value: /api/v2
      backendRefs:
        - name: api-v2-service
          port: 8080
```

### Header-Based Routing

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: header-routing
spec:
  parentRefs:
    - name: example-gateway
  rules:
    - matches:
        - headers:
            - name: X-Version
              value: beta
      backendRefs:
        - name: beta-service
          port: 8080
    - backendRefs:
        - name: stable-service
          port: 8080
```

### Weighted Traffic Splitting

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: canary-deployment
spec:
  parentRefs:
    - name: example-gateway
  rules:
    - backendRefs:
        - name: stable-service
          port: 8080
          weight: 90
        - name: canary-service
          port: 8080
          weight: 10
```

### Multi-Hostname Routing

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: multi-host-routing
spec:
  parentRefs:
    - name: example-gateway
  hostnames:
    - "www.example.com"
    - "api.example.com"
  rules:
    - matches:
        - headers:
            - name: Host
              value: www.example.com
      backendRefs:
        - name: web-service
          port: 80
    - matches:
        - headers:
            - name: Host
              value: api.example.com
      backendRefs:
        - name: api-service
          port: 8080
```

## Development

### Building

```bash
# Build the controller binary
go build -o bin/controller cmd/controller/main.go

# Build Docker image
docker build -t pangolin-gateway-controller:latest .
```

### Running Locally

```bash
# Set environment variables
export PANGOLIN_API_KEY=your-api-key
export PANGOLIN_ORG_ID=your-org-id
export GATEWAY_CLASS_NAME=pangolin
export ENABLE_LEADER_ELECTION=false

# Run the controller
go run cmd/controller/main.go --env-config

# Or with config file
go run cmd/controller/main.go --config config/config.example.yaml
```

### Testing go

```bash
# Run unit tests
go test ./pkg/...

# Run with coverage
go test -cover ./pkg/...

# Integration tests (requires running cluster)
go test -tags=integration ./test/...
```

## Monitoring

### Metrics

The controller exposes Prometheus metrics on `:8080/metrics`:

- `controller_reconcile_duration_seconds` - Time spent reconciling resources
- `controller_reconcile_errors_total` - Total reconciliation errors
- `pangolin_api_requests_total` - Total Pangolin API requests
- `pangolin_api_errors_total` - Total Pangolin API errors

### Health Checks

- **Liveness**: `GET :8081/healthz`
- **Readiness**: `GET :8081/readyz`

## Testing

### Local Testing with Kind

1. **Create kind cluster**

```bash
kind create cluster
```

1. **Install Gateway API CRDs**

```bash
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.4.1/standard-install.yaml
```

1. **Build and load image**

```bash
make docker-build IMG=pangolin-gateway-controller:latest
kind load docker-image pangolin-gateway-controller:latest
```

1. **Set up credentials**

```bash
kubectl create secret generic pangolin-api-credentials \
  --from-literal=apiKey=$PANGOLIN_API_KEY \
  --from-literal=orgId=$PANGOLIN_ORG_ID \
  -n pangolin-system
```

1. **Deploy controller**

```bash
kubectl apply -f config/rbac.yaml
kubectl apply -f config/gatewayclass.yaml
kubectl apply -f config/deployment.yaml
```

1. **Deploy test application**

```bash
kubectl apply -f full-test.yaml
```

1. **Verify deployment**

```bash
# Check controller logs
kubectl logs -n pangolin-system -l app=pangolin-gateway-controller --tail=50

# Check for successful reconciliation
# Expected: "Found existing Pangolin resource" or "Created Pangolin resource"
# Expected: "Target already exists with correct configuration" or "Created target"

# Check VPN status
kubectl logs -l app.kubernetes.io/name=newt -n default
# Expected: "Tunnel connection to server established successfully!"

# Verify site is online
curl -H "Authorization: Bearer $PANGOLIN_API_KEY" \
  https://api.dev0ps.me/v1/org/home/sites | jq '.data.sites[] | {name, online}'
# Expected: {"name": "pgc-example-gateway", "online": true}
```

### Expected Reconciliation Output

When everything is working correctly, you should see logs like:

```log
INFO: Reconciling HTTPRoute example-httproute
INFO: Found existing Pangolin resource resourceID=17 subdomain=test
INFO: Target already exists with correct configuration, skipping ip=10.96.46.192 port=8080 path=/api
INFO: Target already exists with correct configuration, skipping ip=10.96.228.76 port=80 path=/
INFO: Successfully reconciled HTTPRoute resourceID=17
```

When drift is detected and corrected:

```log
INFO: Reconciling HTTPRoute example-httproute
INFO: Target drift detected: priority changed targetId=25 old=20 new=10
INFO: Deleting drifted target targetId=25
INFO: Created target with routing rules targetID=27 ip=10.96.46.192 port=8080 path=/api pathMatchType=prefix priority=10
INFO: Successfully reconciled HTTPRoute resourceID=17
```

## Troubleshooting

### Gateway Not Programmed

```bash
# Check Gateway status
kubectl describe gateway <gateway-name>

# Check controller logs
kubectl logs -n pangolin-system -l app=pangolin-gateway-controller

# Verify Pangolin credentials
kubectl get secret pangolin-api-credentials -n pangolin-system -o yaml
```

### HTTPRoute Not Accepted

```bash
# Check HTTPRoute status
kubectl describe httproute <route-name>

# Verify parent Gateway exists
kubectl get gateway <parent-gateway-name>

# Check if service backends exist
kubectl get svc <backend-service-name>
```

### Pangolin API Errors

```bash
# Enable debug logging
kubectl set env deployment/pangolin-gateway-controller \
  LOG_LEVEL=debug \
  -n pangolin-system

# Check API connectivity
kubectl exec -n pangolin-system deploy/pangolin-gateway-controller -- \
  wget -O- https://api.pangolin.net/v1/
```

## Contributing

Contributions are welcome! Please:

1. Fork the repository
2. Create a feature branch
3. Make your changes with tests
4. Submit a pull request

## License

Apache License 2.0

## Resources

- [Kubernetes Gateway API](https://gateway-api.sigs.k8s.io/)
- [Pangolin Documentation](https://docs.pangolin.net/)
- [Pangolin API Reference](https://api.pangolin.net/v1/docs)

## Support

- GitHub Issues: [github.com/dxas90/pangolin-gateway-controller/issues](https://github.com/dxas90/pangolin-gateway-controller/issues)
- Pangolin Support: [support@pangolin.net](mailto:support@pangolin.net)
