# Image URL to use all building/pushing image targets
IMAGE_TAG_BASE ?= pangolin-gateway-controller
VERSION ?= 0.1.0
IMG ?= $(IMAGE_TAG_BASE):$(VERSION)

# Pin toolchain to match go.mod (avoids golang.org/x/net Go 1.26 stdlib incompatibility)
export GOTOOLCHAIN=go1.25.7

# Build-time version injection
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
VERSION_LDFLAGS := -X main.version=$(VERSION) -X main.buildDate=$(BUILD_DATE)

# Go settings
ENVTEST_K8S_VERSION ?= 1.31.x
GOARCH ?= $(shell go env GOARCH)
GOOS ?= $(shell go env GOOS)

# Container tool
CONTAINER_TOOL ?= docker

# Local bin directory for tools
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# Tool binaries
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT ?= $(LOCALBIN)/golangci-lint

# Tool versions
ENVTEST_VERSION ?= release-0.19
GOLANGCI_LINT_VERSION ?= v1.57.2

.PHONY: all
all: build

##@ General

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: fmt vet envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN)/k8s -p path)" \
		go test ./pkg/... -coverprofile cover.out

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: lint
lint: golangci-lint ## Run golangci-lint.
	$(GOLANGCI_LINT) run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint and perform fixes.
	$(GOLANGCI_LINT) run --fix

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT)
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))

##@ Build

.PHONY: build
build: fmt vet ## Build controller binary.
	go build -ldflags "$(VERSION_LDFLAGS)" -o bin/controller cmd/controller/main.go

.PHONY: run
run: fmt vet ## Run controller from your host.
	go run -ldflags "$(VERSION_LDFLAGS)" cmd/controller/main.go --env-config

.PHONY: docker-build
docker-build: ## Build docker image.
	$(CONTAINER_TOOL) build -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push docker image.
	$(CONTAINER_TOOL) push ${IMG}

.PHONY: docker-buildx
docker-buildx: ## Build and push docker image for cross-platform support.
	- $(CONTAINER_TOOL) buildx create --name project-builder
	$(CONTAINER_TOOL) buildx use project-builder
	- $(CONTAINER_TOOL) buildx build --push --platform linux/arm64,linux/amd64 --tag ${IMG} .
	- $(CONTAINER_TOOL) buildx rm project-builder

##@ Deployment

.PHONY: kind-load
kind-load: ## Load image into kind cluster.
	kind load docker-image ${IMG}

.PHONY: one-local
one-local: docker-build kind-load ## Build image, load into kind, install CRDs, and deploy controller.
	$(MAKE) install
	$(MAKE) deploy

.PHONY: install
install: ## Install Gateway API CRDs.
	kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.5.0/standard-install.yaml

.PHONY: uninstall
uninstall: ## Uninstall Gateway API CRDs.
	kubectl delete -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.5.0/standard-install.yaml

.PHONY: deploy
deploy: ## Deploy controller to the K8s cluster.
	kubectl apply -f config/rbac.yaml
	kubectl apply -f config/gatewayclass.yaml
	kubectl apply -f config/deployment.yaml

.PHONY: undeploy
undeploy: ## Undeploy controller from the K8s cluster.
	kubectl delete -f config/deployment.yaml
	kubectl delete -f config/gatewayclass.yaml
	kubectl delete -f config/rbac.yaml

.PHONY: deploy-examples
deploy-examples: ## Deploy example resources.
	kubectl apply -f examples/backend-services.yaml
	kubectl apply -f examples/gateway.yaml
	kubectl apply -f examples/httproute.yaml

.PHONY: clean-examples
clean-examples: ## Clean up example resources.
	kubectl delete -f examples/httproute.yaml
	kubectl delete -f examples/gateway.yaml
	kubectl delete -f examples/backend-services.yaml

##@ Dependencies

.PHONY: deps
deps: ## Download dependencies.
	go mod download

.PHONY: tidy
tidy: ## Tidy go.mod.
	go mod tidy

.PHONY: vendor
vendor: ## Vendor dependencies.
	go mod vendor

# go-install-tool will 'go install' any package with custom target and name
define go-install-tool
@[ -f "$(1)" ] || { \
set -e; \
echo "Downloading $(2)@$(3)" ;\
GOBIN=$(LOCALBIN) go install $(2)@$(3) ;\
}
endef
