# Pangolin Gateway Controller - Operator Runbook

## Table of Contents

1. [Finalizer Operations](#finalizer-operations)
2. [Graceful Shutdown](#graceful-shutdown)
3. [Troubleshooting](#troubleshooting)
4. [Monitoring & Alerts](#monitoring--alerts)
5. [Emergency Procedures](#emergency-procedures)

## Finalizer Operations

### Understanding Finalizers

The Pangolin Gateway Controller uses finalizers to ensure proper cleanup of Pangolin resources when Kubernetes resources are deleted.

**Finalizers in use:**

- `gateway.pangolin.net/finalizer` - On Gateway resources

### Normal Finalizer Flow

When a Gateway is deleted:

1. Kubernetes sets the `deletionTimestamp`
2. Controller enters deletion logic
3. Controller deletes Pangolin site via API
4. Controller removes finalizer from Gateway
5. Kubernetes completes deletion

### Stuck Finalizer Detection

**Symptoms:**

```bash
# Gateway stuck in "Terminating" state
kubectl get gateway my-gateway
NAME         CLASS      ADDRESS   PROGRAMMED   AGE
my-gateway   pangolin             Unknown      5m    (Terminating)

# Finalizer present
kubectl get gateway my-gateway -o jsonpath='{.metadata.finalizers}'
["gateway.pangolin.net/finalizer"]
```

### Finalizer Removal Procedures

#### ⚠️ WARNING

Only remove finalizers as a last resort. This bypasses cleanup logic and may leave orphaned resources in Pangolin.

#### Procedure 1: Check Controller Logs

```bash
# Check why finalizer cleanup is failing
kubectl logs -n pangolin-system -l app=pangolin-gateway-controller --tail=100 | grep "my-gateway"

# Common failure reasons:
# - Pangolin API unreachable
# - Site already deleted (404)
# - Authentication failure (401)
# - Rate limiting (429)
```

#### Procedure 2: Manual Cleanup (Recommended)

```bash
# 1. Get the site ID from Gateway labels
SITE_ID=$(kubectl get gateway my-gateway -o jsonpath='{.metadata.labels.gateway\.pangolin\.net/site-id}')

# 2. Manually delete site from Pangolin
curl -X DELETE \
  -H "Authorization: Bearer $PANGOLIN_API_KEY" \
  https://api.example.com/v1/site/$SITE_ID

# 3. Remove finalizer
kubectl patch gateway my-gateway \
  -p '{"metadata":{"finalizers":null}}' \
  --type=merge
```

#### Procedure 3: Force Removal (Emergency Only)

**⚠️ USE ONLY IF:**

- Pangolin API is permanently unavailable
- Site is confirmed deleted manually
- Gateway must be removed urgently

```bash
# Force remove finalizer (SKIPS CLEANUP!)
kubectl patch gateway my-gateway \
  -p '{"metadata":{"finalizers":null}}' \
  --type=merge

# Document in incident report:
# - Gateway name: my-gateway
# - Site ID: <site-id from label>
# - Reason for forced removal
# - Verification that site was manually cleaned up
```

#### Procedure 4: Bulk Finalizer Removal

```bash
# List all stuck Gateways
kubectl get gateway -A -o json | \
  jq -r '.items[] | select(.metadata.deletionTimestamp != null) | "\(.metadata.namespace)/\(.metadata.name)"'

# Remove finalizers from all stuck Gateways (DANGEROUS!)
kubectl get gateway -A -o json | \
  jq -r '.items[] | select(.metadata.deletionTimestamp != null) | "\(.metadata.namespace) \(.metadata.name)"' | \
  while read ns name; do
    echo "Removing finalizer from $ns/$name"
    kubectl patch gateway -n $ns $name -p '{"metadata":{"finalizers":null}}' --type=merge
  done
```

---

## Graceful Shutdown

### Controller Shutdown Process

The controller respects Kubernetes termination signals (SIGTERM) for graceful shutdown:

1. **SIGTERM received** - Kubernetes sends termination signal
2. **Leader election released** - Standby becomes active
3. **In-flight reconciliations finish** - Up to `terminationGracePeriodSeconds`
4. **Clean exit** - Process exits with code 0

### Configuration

```yaml
# In deployment.yaml
spec:
  template:
    spec:
      terminationGracePeriodSeconds: 30  # Allow 30s for cleanup
      containers:
      - name: controller
        # Controller automatically handles SIGTERM
```

### Verifying Graceful Shutdown

```bash
# Watch pod termination
kubectl logs -n pangolin-system -l app=pangolin-gateway-controller --follow

# Expected log output:
# "Stopping controller" - Shutdown initiated
# "Leader election lost" - Leadership released
# "Shutting down workers" - Reconciliation stopped
# "Shutdown complete" - Clean exit
```

### Force Termination

```bash
# If controller doesn't stop after terminationGracePeriodSeconds
kubectl delete pod -n pangolin-system pangolin-gateway-controller-xxx --grace-period=0 --force

# ⚠️ WARNING: In-flight reconciliations will be interrupted
# May cause inconsistent state if reconciliation was mid-update
```

---

## Troubleshooting

### Gateway Stuck in `Programmed=false`

**Symptom:**

```bash
kubectl get gateway my-gateway
# NAME         CLASS      ADDRESS   PROGRAMMED   AGE
# my-gateway   pangolin             False        3m

kubectl describe gateway my-gateway | grep -A5 'Conditions'
# Type: Programmed
# Status: False
# Reason: Invalid
# Message: failed to create Pangolin site: ...
```

**Diagnosis steps:**

```bash
# Step 1: Read the condition message
kubectl get gateway my-gateway -o jsonpath='{.status.conditions[?(@.type=="Programmed")].message}'

# Step 2: Check controller logs for this Gateway
kubectl logs -n pangolin-system -l app=pangolin-gateway-controller --tail=200 \
  | grep 'my-gateway'

# Step 3: Verify API credentials are valid
SECRET=$(kubectl get secret pangolin-api-credentials -n pangolin-system \
  -o jsonpath='{.data.apiKey}' | base64 -d)
curl -sI -H "Authorization: Bearer $SECRET" \
  ${PANGOLIN_BASE_URL}/org/${PANGOLIN_ORG_ID}/sites | head -1
# Expected: HTTP/2 200
# 401 → credentials invalid/expired
# 000 → network unreachable

# Step 4: Check the GatewayClass exists
kubectl get gatewayclass pangolin -o yaml
```

**Common causes and fixes:**

| Message | Cause | Fix |
|---|---|---|
| `401 Unauthorized` | API key expired or wrong key | Update `pangolin-api-credentials` secret |
| `connection refused` / `no such host` | Wrong `PANGOLIN_BASE_URL` | Verify env var points to Integration API |
| `site already exists` | Previous partial creation | Controller will find and reuse it automatically |
| `GatewayClass not found` | CRD or class missing | `kubectl apply -f config/gatewayclass.yaml` |

---

### Newt VPN Fails to Connect

**Symptom:** Traffic not reaching backends even though Gateway is `Programmed=True`.

```bash
# Check newt pod logs
kubectl logs -l app.kubernetes.io/name=newt -n default --tail=100
# Error examples:
#  "401 Unauthorized" → wrong credentials or endpoint
#  "dial tcp: no such host" → NEWT_ENDPOINT is wrong
#  "405 Method Not Allowed" → newt connecting to Integration API instead of Pangolin server
```

**Diagnosis steps:**

```bash
# Step 1: Find the newt credential Secret for your Gateway
kubectl get secret my-gateway-newt-cred -n default -o yaml

# Step 2: Decode and verify PANGOLIN_ENDPOINT value
# It MUST be https://pangolin.example.com — NOT https://api.example.com/v1
kubectl get secret my-gateway-newt-cred -n default \
  -o jsonpath='{.data.PANGOLIN_ENDPOINT}' | base64 -d
echo

# Step 3: Verify NEWT_ID is non-empty
kubectl get secret my-gateway-newt-cred -n default \
  -o jsonpath='{.data.NEWT_ID}' | base64 -d
echo

# Step 4: If either value is wrong, delete the Secret to force recreation
kubectl delete secret my-gateway-newt-cred -n default
# The controller will recreate it on next reconciliation
```

**NEWT_ENDPOINT vs PANGOLIN_BASE_URL — the most common mistake:**

```
PANGOLIN_BASE_URL = https://api.example.com/v1   ← controller uses this
NEWT_ENDPOINT     = https://pangolin.example.com  ← newt pods connect here
```

If you set `NEWT_ENDPOINT` manually (e.g. via Helm `controller.newtEndpoint`), make sure it matches the Pangolin server hostname, not the Integration API.

---

### HTTPRoute Not Routing Traffic

**Symptom:** HTTPRoute exists but requests return 404 or reach wrong backend.

```bash
# Check HTTPRoute status
kubectl describe httproute my-route
# Look for: parents[].conditions[type=Accepted].status
# and:       parents[].conditions[type=ResolvedRefs].status

# Check hostname matching — skipped hostnames appear in controller logs:
kubectl logs -n pangolin-system -l app=pangolin-gateway-controller --tail=100 \
  | grep -i 'skipping hostname\|no matching domain'
# If you see: "Skipping hostname that doesn't match any Pangolin domain"
# It means the hostname is not registered in your Pangolin org.
```

**Verify from Pangolin side:**

```bash
# List Pangolin resources for your org
curl -s -H "Authorization: Bearer $PANGOLIN_API_KEY" \
  ${PANGOLIN_BASE_URL}/org/${PANGOLIN_ORG_ID}/resources | jq '.data.resources[] | {name,id}'

# List targets for a given resource
RESOURCE_ID=<id from above>
curl -s -H "Authorization: Bearer $PANGOLIN_API_KEY" \
  ${PANGOLIN_BASE_URL}/resource/${RESOURCE_ID}/targets | jq '.data.targets[]'
```

---

### High Reconciliation Latency

**Symptom:** `controller_runtime_reconcile_time_seconds` p99 > 5s

**Causes:**

1. Pangolin API slow/degraded
2. Too many resources
3. Insufficient controller resources

**Resolution:**

```bash
# Check API latency
kubectl logs -n pangolin-system -l app=pangolin-gateway-controller | grep "API request"

# Check controller resource usage
kubectl top pod -n pangolin-system -l app=pangolin-gateway-controller

# Increase resources if CPU/memory constrained
kubectl set resources deployment pangolin-gateway-controller \
  -n pangolin-system \
  --requests=cpu=500m,memory=512Mi \
  --limits=cpu=1000m,memory=1Gi

# Scale replicas for leader election failover
kubectl scale deployment pangolin-gateway-controller -n pangolin-system --replicas=2
```

### High Error Rate

**Symptom:** `pangolin_gateway_reconcile_total{result="error"}` increasing

**Diagnosis:**

```bash
# Check error types
kubectl logs -n pangolin-system -l app=pangolin-gateway-controller --tail=100 | grep "error"

# Common errors:
# - "401 Unauthorized" → API key invalid/expired
# - "404 Not Found" → Site/resource deleted externally
# - "429 Too Many Requests" → Rate limiting
# - "503 Service Unavailable" → Pangolin API down
```

**Resolution by Error Type:**

| Error Code | Resolution |
| ---------- | ---------- |
| 401 | Rotate API key in Secret |
| 404 | Resource deleted - will recreate |
| 429 | Reduce MaxConcurrentReconciles |
| 503 | Wait for Pangolin recovery |

### Workqueue Depth Growing

**Symptom:** `workqueue_depth` > 100

**Meaning:** Reconciliations backing up, controller can't keep pace

**Resolution:**

```bash
# Increase MaxConcurrentReconciles
# Edit controller code and redeploy with higher concurrency

# Or reduce load
kubectl delete httproute -A -l app=test  # Remove test routes
```

---

## Monitoring & Alerts

### Critical Metrics

```promql
# High error rate (>10% of reconciliations failing)
rate(pangolin_gateway_reconcile_total{result="error"}[5m]) /
rate(pangolin_gateway_reconcile_total[5m]) > 0.1

# High API latency (p95 > 5s)
histogram_quantile(0.95,
  rate(pangolin_api_request_duration_seconds_bucket[5m])
) > 5

# Workqueue backing up (depth > 50)
workqueue_depth > 50

# Controller down
up{job="pangolin-gateway-controller"} == 0
```

### Recommended Alerts

```yaml
# alerts.yaml
groups:
- name: pangolin_controller
  interval: 30s
  rules:
  - alert: PangolinControllerDown
    expr: up{job="pangolin-gateway-controller"} == 0
    for: 2m
    labels:
      severity: critical
    annotations:
      summary: "Pangolin Gateway Controller is down"

  - alert: PangolinHighErrorRate
    expr: |
      rate(pangolin_gateway_reconcile_total{result="error"}[5m]) /
      rate(pangolin_gateway_reconcile_total[5m]) > 0.1
    for: 5m
    labels:
      severity: warning
    annotations:
      summary: "High reconciliation error rate"

  - alert: PangolinAPIHighLatency
    expr: |
      histogram_quantile(0.95,
        rate(pangolin_api_request_duration_seconds_bucket[5m])
      ) > 5
    for: 5m
    labels:
      severity: warning
    annotations:
      summary: "Pangolin API latency is high"

  - alert: PangolinWorkqueueBackup
    expr: workqueue_depth{name="gateway"} > 50
    for: 10m
    labels:
      severity: warning
    annotations:
      summary: "Gateway reconciliation queue backing up"
```

---

## Emergency Procedures

### Complete Controller Failure

**Scenario:** Controller crashed, won't start, or corrupt state

**Procedure:**

```bash
# 1. Scale down to zero (stop reconciliation)
kubectl scale deployment pangolin-gateway-controller -n pangolin-system --replicas=0

# 2. Check for stuck finalizers
kubectl get gateway -A -o json | \
  jq -r '.items[] | select(.metadata.deletionTimestamp != null)'

# 3. Manually clean up Pangolin resources if needed
# (See "Manual Cleanup" section above)

# 4. Check for configuration issues
kubectl get secret pangolin-api-credentials -n pangolin-system -o yaml
kubectl get configmap -n pangolin-system

# 5. Scale back up
kubectl scale deployment pangolin-gateway-controller -n pangolin-system --replicas=2

# 6. Monitor recovery
kubectl logs -n pangolin-system -l app=pangolin-gateway-controller --follow
```

### Pangolin API Outage

**Scenario:** Pangolin API is down/unreachable

**Impact:**

- New Gateways stuck in "Pending"
- HTTPRoute updates not propagated
- Gateway deletions stuck (finalizers)

**Procedure:**

```bash
# 1. Verify API is down
curl -H "Authorization: Bearer $PANGOLIN_API_KEY" \
  https://api.example.com/v1/org/$ORG_ID/sites

# 2. Check circuit breaker state (if implemented)
# Metrics should show PangolinAPIErrors increasing

# 3. Scale down controller to prevent error storm
kubectl scale deployment pangolin-gateway-controller -n pangolin-system --replicas=0

# 4. Wait for Pangolin API recovery

# 5. Scale up controller
kubectl scale deployment pangolin-gateway-controller -n pangolin-system --replicas=2

# 6. Controller will automatically reconcile all resources
```

### Mass Resource Deletion

**Scenario:** Many Gateways/Routes deleted at once

**Risk:** Finalizer cleanup may overwhelm Pangolin API

**Procedure:**

```bash
# 1. Monitor API error rate
kubectl logs -n pangolin-system -l app=pangolin-gateway-controller | grep "429"

# 2. If rate limited, temporarily scale down
kubectl scale deployment pangolin-gateway-controller -n pangolin-system --replicas=1

# 3. Deletions will proceed at controlled pace

# 4. Once queue clears, scale back up
kubectl scale deployment pangolin-gateway-controller -n pangolin-system --replicas=2
```

---

## Health Checks

### Controller Health Endpoints

```bash
# Liveness probe (is controller running?)
curl http://localhost:8081/healthz

# Readiness probe (is controller ready to reconcile?)
curl http://localhost:8081/readyz

# Expected: 200 OK
```

### Manual Health Verification

```bash
# 1. Check pod status
kubectl get pods -n pangolin-system -l app=pangolin-gateway-controller

# 2. Check logs for errors
kubectl logs -n pangolin-system -l app=pangolin-gateway-controller --tail=50

# 3. Check metrics endpoint
kubectl port-forward -n pangolin-system svc/pangolin-gateway-controller-metrics 8080:8080
curl http://localhost:8080/metrics | grep pangolin_

# 4. Verify Pangolin API connectivity
kubectl exec -it -n pangolin-system $(kubectl get pod -n pangolin-system -l app=pangolin-gateway-controller -o name | head -1) -- \
  curl -H "Authorization: Bearer $PANGOLIN_API_KEY" https://api.example.com/v1/org/$ORG_ID/sites
```

---

## Post-Incident Checklist

After resolving an incident:

- [ ] Document root cause
- [ ] Update runbook if new procedure discovered
- [ ] Review metrics for anomalies
- [ ] Verify all stuck resources cleared
- [ ] Check for orphaned Pangolin resources
- [ ] Update alerts if incident was preventable
- [ ] Conduct blameless postmortem
