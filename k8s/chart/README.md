# Pangolin Gateway Controller Helm Chart

This Helm chart deploys the Pangolin Gateway Controller to a Kubernetes cluster.

## Prerequisites

- Kubernetes 1.25+
- Helm 3.8+
- Gateway API CRDs v1.4.1+ installed

## Installing Gateway API CRDs

Before installing this chart, you must install the Gateway API CRDs:

```bash
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.4.1/standard-install.yaml
```

## Installing the Chart

### Create namespace

```bash
kubectl create namespace pangolin-system
```

### Install with Helm

```bash
helm install pangolin-gateway-controller ./k8s/chart \
  --namespace pangolin-system \
  --set pangolin.apiKey=YOUR_API_KEY \
  --set pangolin.orgId=YOUR_ORG_ID \
  --set pangolin.baseUrl=https://api.pangolin.net/v1
```

Or using a values file:

```bash
cat > my-values.yaml <<EOF
pangolin:
  apiKey: "YOUR_API_KEY"
  orgId: "YOUR_ORG_ID"
  baseUrl: "https://api.pangolin.net/v1"
EOF

helm install pangolin-gateway-controller ./k8s/chart \
  --namespace pangolin-system \
  --values my-values.yaml
```

## Configuration

### Required Parameters

| Parameter | Description | Example |
| --------- | ----------- | ------- |
| `pangolin.apiKey` | Pangolin API key | `sk_xxx` |
| `pangolin.orgId` | Pangolin organization ID | `home` |
| `pangolin.baseUrl` | Pangolin Integration API base URL | `https://api.pangolin.net/v1` |

### Optional Parameters

| Parameter | Description | Default |
| --------- | ----------- | ------- |
| `replicaCount` | Number of controller replicas | `1` |
| `image.repository` | Controller image repository | `ghcr.io/dxas90/pangolin-gateway-controller` |
| `image.tag` | Controller image tag | `latest` |
| `image.pullPolicy` | Image pull policy | `IfNotPresent` |
| `pangolin.newtEndpoint` | Newt VPN endpoint (auto-derived if empty) | `""` |
| `pangolin.newtImage` | Newt VPN image | `fosrl/newt:1.10.0` |
| `controller.gatewayClassName` | GatewayClass name to watch | `pangolin` |
| `controller.watchNamespace` | Namespace to watch (empty = all) | `""` |
| `controller.leaderElection` | Enable leader election | `true` |
| `logging.level` | Log level (debug, info, warn, error) | `info` |
| `logging.format` | Log format (json, console) | `json` |
| `resources.limits.cpu` | CPU limit | `500m` |
| `resources.limits.memory` | Memory limit | `256Mi` |
| `resources.requests.cpu` | CPU request | `100m` |
| `resources.requests.memory` | Memory request | `128Mi` |

### Security Configuration

The controller runs with a restrictive security context:

```yaml
securityContext:
  runAsNonRoot: true
  runAsUser: 65532
  readOnlyRootFilesystem: true
  allowPrivilegeEscalation: false
  capabilities:
    drop:
      - ALL
```

### RBAC Configuration

The chart creates the following RBAC resources:

- **ClusterRole**: Permissions for Gateway API resources, Secrets, Deployments, Services
- **ClusterRoleBinding**: Binds ClusterRole to ServiceAccount
- **Role**: Leader election permissions in the controller namespace
- **RoleBinding**: Binds Role to ServiceAccount

RBAC can be disabled by setting `rbac.create=false`, but this is not recommended.

## Verifying the Installation

Check that the controller is running:

```bash
kubectl get pods -n pangolin-system -l app.kubernetes.io/name=pangolin-gateway-controller
```

Check controller logs:

```bash
kubectl logs -n pangolin-system -l app.kubernetes.io/name=pangolin-gateway-controller -f
```

Run Helm tests:

```bash
helm test pangolin-gateway-controller -n pangolin-system
```

## Creating a GatewayClass

After installing the controller, create a GatewayClass:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: pangolin
spec:
  controllerName: pangol.in/gateway-controller
```

Apply with:

```bash
kubectl apply -f https://raw.githubusercontent.com/dxas90/pangolin-gateway-controller/main/config/gatewayclass.yaml
```

## Uninstalling the Chart

```bash
helm uninstall pangolin-gateway-controller -n pangolin-system
```

**Note**: This will not delete CRDs or GatewayClass resources. To clean up completely:

```bash
# Delete any Gateway resources first
kubectl delete gateways --all -A

# Uninstall the chart
helm uninstall pangolin-gateway-controller -n pangolin-system

# Optional: Delete the namespace
kubectl delete namespace pangolin-system
```

## Troubleshooting

### Controller won't start

Check that the API credentials are correct:

```bash
kubectl get secret -n pangolin-system pangolin-gateway-controller-credentials -o yaml
```

### Newt VPN won't connect

Check the newt deployment logs:

```bash
kubectl logs -n <namespace> -l app.kubernetes.io/name=newt --tail=50
```

Common errors:
- 401: Invalid credentials - check `PANGOLIN_API_KEY`
- 405: Wrong endpoint - check `NEWT_ENDPOINT` configuration
- Connection timeout: Network or firewall issues

### Gateway stuck in non-Programmed state

Describe the Gateway to see error conditions:

```bash
kubectl describe gateway <gateway-name>
```

## Contributing

For bugs and feature requests, please create an issue at:
https://github.com/dxas90/pangolin-gateway-controller/issues

## License

See the [LICENSE](../../LICENSE) file for details.
