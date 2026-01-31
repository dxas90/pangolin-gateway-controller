# Development Guide

## Getting Started

### Prerequisites

- Go 1.21 or later
- Docker (for building images)
- kubectl and access to a Kubernetes cluster
- Pangolin account with API access

### Local Development Setup

1. **Clone the repository**

```bash
git clone https://github.com/example/pangolin-gateway-controller
cd pangolin-gateway-controller
```

1. **Install dependencies**

```bash
make deps
```

1. **Set up environment variables**

```bash
export PANGOLIN_API_KEY=your-api-key
export PANGOLIN_ORG_ID=your-org-id
export GATEWAY_CLASS_NAME=pangolin
export ENABLE_LEADER_ELECTION=false
export LOG_LEVEL=debug
```

1. **Run the controller locally**

```bash
make run
```

## Project Structure

```
pangolin-gateway-controller/
├── cmd/
│   └── controller/
│       └── main.go                 # Main entry point
├── pkg/
│   ├── config/
│   │   └── config.go              # Configuration management
│   ├── controller/
│   │   ├── gateway_controller.go  # Gateway reconciler
│   │   └── httproute_controller.go # HTTPRoute reconciler
│   └── pangolin/
│       └── client.go              # Pangolin API client
├── config/
│   ├── gatewayclass.yaml          # GatewayClass definition
│   ├── rbac.yaml                  # RBAC permissions
│   ├── deployment.yaml            # Kubernetes deployment
│   └── config.example.yaml        # Example configuration
├── examples/
│   ├── gateway.yaml               # Example Gateway
│   ├── httproute.yaml             # Example HTTPRoute
│   ├── canary.yaml                # Canary deployment example
│   └── backend-services.yaml      # Test backend services
├── docs/
│   └── ARCHITECTURE.md            # Architecture documentation
├── Dockerfile                     # Container image definition
├── Makefile                       # Build automation
├── go.mod                         # Go module definition
├── go.sum                         # Go module checksums
├── LICENSE                        # Apache 2.0 license
└── README.md                      # Main documentation
```

## Building

### Build Binary

```bash
make build
```

The binary will be created at `bin/controller`.

### Build Docker Image

```bash
make docker-build IMG=myrepo/pangolin-gateway-controller:v0.1.0
```

### Push Docker Image

```bash
make docker-push IMG=myrepo/pangolin-gateway-controller:v0.1.0
```

## Testing

### Run Unit Tests

```bash
make test
```

### Run with Coverage

```bash
go test -cover -coverprofile=coverage.out ./pkg/...
go tool cover -html=coverage.out
```

### Manual Testing

1. **Deploy test services**

```bash
kubectl apply -f examples/backend-services.yaml
```

1. **Create a Gateway**

```bash
kubectl apply -f examples/gateway.yaml
```

1. **Check Gateway status**

```bash
kubectl get gateway example-gateway -o yaml
kubectl describe gateway example-gateway
```

1. **Create an HTTPRoute**

```bash
kubectl apply -f examples/httproute.yaml
```

1. **Check HTTPRoute status**

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
make vet
```

### Install golangci-lint

```bash
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
make lint
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
3. Add the controller setup in `cmd/controller/main.go`
4. Update RBAC permissions in `config/rbac.yaml`

### Adding New Pangolin API Methods

1. Add method signatures to `pkg/pangolin/client.go`
2. Implement the HTTP request logic
3. Add tests for the new methods

## Contributing

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Make your changes
4. Add tests for new functionality
5. Run tests and linting (`make test lint`)
6. Commit your changes (`git commit -m 'Add amazing feature'`)
7. Push to the branch (`git push origin feature/amazing-feature`)
8. Open a Pull Request

### Commit Message Guidelines

Follow conventional commits:

- `feat: Add new feature`
- `fix: Fix bug`
- `docs: Update documentation`
- `test: Add tests`
- `refactor: Refactor code`
- `chore: Update dependencies`

## Release Process

1. Update version in relevant files
2. Update CHANGELOG.md
3. Create a git tag (`git tag v0.1.0`)
4. Push the tag (`git push origin v0.1.0`)
5. Build and push Docker images
6. Create GitHub release with notes

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
