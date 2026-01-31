# Helm Chart Testing Guide

This guide explains how to test the Helm chart for the Pangolin Gateway Controller.

## Prerequisites

- Kubernetes cluster (kind, minikube, or cloud provider)
- Helm 3.8+
- kubectl configured to access your cluster

## Quick Test with Kind

### 1. Create a Kind cluster

```bash
kind create cluster --name pgc-test
```

### 2. Install Gateway API CRDs

```bash
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.4.1/standard-install.yaml
```

### 3. Create namespace

```bash
kubectl create namespace pangolin-system
```

### 4. Lint the chart

```bash
helm lint k8s/chart/
```

Expected output:

```
==> Linting k8s/chart/
[INFO] Chart.yaml: icon is recommended

1 chart(s) linted, 0 chart(s) failed
```

### 5. Test template rendering

```bash
helm template test-release k8s/chart/ \
  --namespace pangolin-system \
  --set pangolin.apiKey=test-key \
  --set pangolin.orgId=test-org \
  --dry-run
```

This should output valid Kubernetes manifests without errors.

### 6. Install the chart (dry-run)

```bash
helm install pangolin-gateway-controller k8s/chart/ \
  --namespace pangolin-system \
  --set pangolin.apiKey=test-key \
  --set pangolin.orgId=test-org \
  --dry-run --debug
```

### 7. Install for real (requires valid Pangolin credentials)

```bash
helm install pangolin-gateway-controller k8s/chart/ \
  --namespace pangolin-system \
  --set pangolin.apiKey=YOUR_API_KEY \
  --set pangolin.orgId=YOUR_ORG_ID \
  --set pangolin.baseUrl=https://api.pangolin.net/v1
```

### 8. Verify installation

```bash
# Check controller pod is running
kubectl get pods -n pangolin-system

# Check logs
kubectl logs -n pangolin-system -l app.kubernetes.io/name=pangolin-gateway-controller -f

# Check RBAC resources
kubectl get clusterrole,clusterrolebinding,role,rolebinding -n pangolin-system | grep pangolin

# Check service
kubectl get svc -n pangolin-system
```

### 9. Run Helm tests

```bash
helm test pangolin-gateway-controller -n pangolin-system
```

Expected output:

```
NAME: pangolin-gateway-controller
LAST DEPLOYED: ...
NAMESPACE: pangolin-system
STATUS: deployed
REVISION: 1
TEST SUITE:     pangolin-gateway-controller-test-connection
Last Started:   ...
Last Completed: ...
Phase:          Succeeded
```

## Manual Testing Checklist

### ✅ Chart Linting

- [ ] `helm lint k8s/chart/` passes without errors
- [ ] All required values documented
- [ ] No deprecated Kubernetes API versions used

### ✅ Template Rendering

- [ ] `helm template` generates valid YAML
- [ ] All conditionals work correctly (rbac.create, etc.)
- [ ] Image references are correct
- [ ] Environment variables properly set from values

### ✅ Installation

- [ ] Chart installs without errors
- [ ] Controller pod starts successfully
- [ ] Secret created with credentials
- [ ] ServiceAccount created
- [ ] RBAC resources created (ClusterRole, ClusterRoleBinding, Role, RoleBinding)
- [ ] Service created for metrics and health

### ✅ Controller Functionality

- [ ] Controller logs show successful startup
- [ ] Leader election works (if enabled)
- [ ] Health probes respond successfully
- [ ] Metrics endpoint accessible

### ✅ Security

- [ ] Pod runs as non-root user (65532)
- [ ] Read-only root filesystem
- [ ] All capabilities dropped
- [ ] No privilege escalation allowed

### ✅ Gateway Integration

- [ ] GatewayClass can be created
- [ ] Gateway resources reconcile successfully
- [ ] Newt VPN deploys correctly
- [ ] HTTPRoute resources reconcile
- [ ] GRPCRoute resources reconcile

### ✅ Cleanup

- [ ] `helm uninstall` removes all resources
- [ ] No orphaned resources remain
- [ ] Finalizers properly handle deletion

## Testing with Different Configurations

### Test with Custom Values

Create a test values file:

```yaml
# test-values.yaml
replicaCount: 2

resources:
  limits:
    cpu: 1000m
    memory: 512Mi
  requests:
    cpu: 200m
    memory: 256Mi

controller:
  leaderElection: true
  watchNamespace: "default"

logging:
  level: "debug"
  format: "console"

pangolin:
  apiKey: "test-key"
  orgId: "test-org"
  baseUrl: "https://api.example.com/v1"
  newtImage: "fosrl/newt:latest"
```

Install with custom values:

```bash
helm install test-release k8s/chart/ \
  --namespace pangolin-system \
  --values test-values.yaml
```

### Test Resource Limits

Verify resource limits are applied:

```bash
kubectl get pods -n pangolin-system -o jsonpath='{.items[*].spec.containers[*].resources}'
```

### Test RBAC Permissions

Verify the controller has necessary permissions:

```bash
# Check ClusterRole
kubectl describe clusterrole pangolin-gateway-controller-manager

# Check if controller can list Gateways
kubectl auth can-i list gateways.gateway.networking.k8s.io \
  --as=system:serviceaccount:pangolin-system:pangolin-gateway-controller
```

## Automated Testing

You can create automated tests using Helm unittest plugin:

### Install Helm unittest plugin

```bash
helm plugin install https://github.com/helm-unittest/helm-unittest
```

### Run tests

```bash
helm unittest k8s/chart/
```

## Troubleshooting Test Failures

### Helm lint failures

- Check Chart.yaml syntax
- Verify all template files have valid YAML
- Check for required field violations

### Template rendering failures

- Verify all referenced values exist in values.yaml
- Check for typos in template function calls
- Ensure conditionals are properly structured

### Installation failures

- Check Kubernetes API version compatibility
- Verify CRDs are installed (Gateway API)
- Check namespace exists
- Verify credentials are valid

### Controller startup failures

- Check logs: `kubectl logs -n pangolin-system -l app.kubernetes.io/name=pangolin-gateway-controller`
- Verify API credentials are correct
- Check RBAC permissions
- Verify image can be pulled

## Performance Testing

### Resource Usage

Monitor resource usage:

```bash
kubectl top pods -n pangolin-system
```

### Reconciliation Speed

Create multiple Gateways/HTTPRoutes and measure reconciliation time:

```bash
time kubectl apply -f examples/
```

## Cleanup After Testing

```bash
# Uninstall chart
helm uninstall pangolin-gateway-controller -n pangolin-system

# Delete namespace
kubectl delete namespace pangolin-system

# Delete kind cluster (if using kind)
kind delete cluster --name pgc-test
```

## CI/CD Integration

Example GitHub Actions workflow:

```yaml
name: Helm Chart Test
on: [push, pull_request]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: azure/setup-helm@v4
      - name: Lint chart
        run: helm lint k8s/chart/
      - name: Template rendering
        run: |
          helm template test k8s/chart/ \
            --set pangolin.apiKey=test \
            --set pangolin.orgId=test \
            --dry-run
```

## Reporting Issues

When reporting chart issues, include:

1. Helm version: `helm version`
2. Kubernetes version: `kubectl version`
3. Chart values used
4. Full error output
5. Controller logs (if applicable)
6. Output of `helm get values <release-name>`
