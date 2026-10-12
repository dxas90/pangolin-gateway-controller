# Development Guide

## Getting Started

### Prerequisites

- Go 1.27 or later
- Docker (for building images)
- kubectl and access to a Kubernetes cluster
- Pangolin account with API access

### Local Development Setup

1. **Clone the repository**

```bash
git clone https://github.com/dxas90/pangolin-gateway-controller
cd pangolin-gateway-controller
```

2. **Install dependencies**

```bash
make deps
```

3. **Set up environment variables**

```bash
export PANGOLIN_API_KEY=your-api-key
export PANGOLIN_ORG_ID=your-org-id
export GATEWAY_CLASS_NAME=pangolin
export ENABLE_LEADER_ELECTION=false
export LOG_LEVEL=debug
```

4. **Run the controller locally**

```bash
make run
```

## Project Structure

```text
pangolin-gateway-controller/
├── cmd/
│   └── controller/
│       └── main.go                       # Main entry point
├── pkg/
│   ├── config/
│   │   └── config.go                     # Configuration management
│   ├── controller/
│   │   ├── gateway_controller.go         # Gateway reconciler (site + credentials)
│   │   ├── newt_controller.go            # Newt VPN reconciler (WireGuard deployment)
│   │   ├── httproute_controller.go       # HTTPRoute reconciler
│   │   ├── httproute_helpers.go          # HTTPRoute helper functions
│   │   ├── httproute_targets.go          # HTTPRoute target reconciliation
│   │   ├── httproute_status.go           # HTTPRoute status updates
│   │   ├── grpcroute_controller.go       # GRPCRoute reconciler (TCP/UDP)
│   │   ├── gatewayclass_controller.go    # GatewayClass reconciler
│   │   ├── healthcheck.go                # Readiness probe with live API check
│   │   ├── ratelimiter.go                # Exponential backoff rate limiter
│   │   └── indexes.go                    # Field indexes for O(1) lookups
│   ├── metrics/
│   │   └── metrics.go                    # Prometheus metrics definitions
│   ├── pangolin/
│   │   ├── client.go                     # Pangolin REST API client
│   │   ├── interface.go                  # ClientInterface definition
│   │   ├── errors.go                     # PangolinAPIError type
│   │   └── circuit_breaker.go            # Circuit breaker for API calls
│   └── testutil/
│       ├── envtest.go                    # Envtest base suite
│       ├── fixtures.go                   # Test data builders
│       └── mock_pangolin.go              # Mock Pangolin client
├── config/
│   ├── gatewayclass.yaml                 # GatewayClass definition
│   ├── rbac.yaml                         # RBAC permissions
│   ├── deployment.yaml                   # Kubernetes deployment
│   └── config.example.yaml              # Example configuration
├── examples/
│   ├── gateway.yaml                      # Example Gateway
│   ├── httproute.yaml                    # Example HTTPRoute
│   ├── grpcroute.yaml                    # Example GRPCRoute
│   ├── canary.yaml                       # Canary deployment example
│   └── backend-services.yaml            # Test backend services
├── k8s/
│   └── chart/                            # Helm chart
├── docs/
│   ├── ARCHITECTURE.md                   # Architecture documentation
│   └── DEVELOPMENT.md                    # This file
├── Dockerfile                            # Container image definition
├── Makefile                              # Build automation
├── go.mod                                # Go module definition
├── go.sum                                # Go module checksums
├── LICENSE                               # Apache 2.0 license
└── README.md                             # Main documentation
```

## Building

### Build Binary

```bash
make build
```

The binary will be created at `bin/controller`.

### Build Docker Image

```bash
make docker-build IMG=myrepo/pangolin-gateway-controller:v0.2.0
```

### Push Docker Image

```bash
make docker-push IMG=myrepo/pangolin-gateway-controller:v0.2.0
```

## Testing

### Run Unit and Integration Tests

```bash
make test
```

This runs all tests (unit + envtest integration) and generates `cover.out`.

### Run with Coverage

```bash
go test -cover -coverprofile=coverage.out ./pkg/...
go tool cover -html=coverage.out
```

Coverage targets: `pkg/pangolin` ≥96%, `pkg/controller` ≥74%.

### Manual Testing

1. **Deploy test services**

```bash
kubectl apply -f examples/backend-services.yaml
```

2. **Create a Gateway**

```bash
kubectl apply -f examples/gateway.yaml
```

3. **Check Gateway status**

```bash
kubectl get gateway example-gateway -o yaml
kubectl describe gateway example-gateway
```

4. **Create an HTTPRoute**

```bash
kubectl apply -f examples/httproute.yaml
```

5. **Check HTTPRoute status**

```bash
kubectl get httproute example-httproute -o yaml
kubectl describe httproute example-httproute
```

## Code Style

### Formatting

```bash
make fmt
```

### Linting

```bash
make lint
```

This runs `go vet`, `golangci-lint`, and `staticcheck`. Install golangci-lint:

```bash
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
```

## Debugging

### Enable Debug Logging

Set `LOG_LEVEL=debug` in your environment or configuration file.

### View Controller Logs

```bash
kubectl logs -n pangolin-system -l app=pangolin-gateway-controller -f
```

### Debug with Delve

```bash
dlv debug cmd/controller/main.go -- --env-config
```

## Adding New Features

### Adding a New Controller

1. Create a new file in `pkg/controller/`
2. Implement the `Reconcile` method
3. Register the controller in `cmd/controller/main.go`
4. Update RBAC permissions in `config/rbac.yaml`
5. Add tests covering happy path, deletion, and error cases

### Adding New Pangolin API Methods

1. Add the method signature to `pkg/pangolin/interface.go`
2. Implement the HTTP request logic in `pkg/pangolin/client.go`
3. Add corresponding tests in `pkg/pangolin/client_test.go`

## Contributing

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Make your changes
4. Add tests for new functionality
5. Run tests and linting (`make test lint`)
6. Commit your changes following [Conventional Commits](https://www.conventionalcommits.org/):
   - `feat: add new feature`
   - `fix: fix bug`
   - `docs: update documentation`
   - `test: add tests`
   - `refactor: refactor code`
   - `chore: update dependencies`
7. Push to the branch (`git push origin feature/amazing-feature`)
8. Open a Pull Request

## Release Process

1. Update `CHANGELOG.md` with changes under `[Unreleased]`
2. Bump the version in `Makefile` (`VERSION ?= X.Y.Z`)
3. Create a git tag: `git tag vX.Y.Z`
4. Push the tag: `git push origin vX.Y.Z`
5. Build and push Docker images: `make docker-build docker-push IMG=ghcr.io/dxas90/pangolin-gateway-controller:vX.Y.Z`
6. Create a GitHub release with the CHANGELOG notes for this version

## Troubleshooting Development Issues

### "Module not found" errors

```bash
make tidy
make deps
```

### Controller not receiving events

- Check RBAC permissions are correctly applied
- Verify GatewayClass name matches
- Check namespace restrictions

### Pangolin API errors

- Verify API key is correct
- Check organization ID
- Ensure API permissions are granted
- Check network connectivity to Pangolin API

## Resources

- [controller-runtime](https://pkg.go.dev/sigs.k8s.io/controller-runtime)
- [Gateway API](https://gateway-api.sigs.k8s.io/)
- [Pangolin API Docs](https://api.pangolin.net/v1/docs)
- [Go Best Practices](https://go.dev/doc/effective_go)
