# Image URL to use all building/pushing image targets
IMG ?= pangolin-gateway-controller:latest

# Pin toolchain to match go.mod (avoids golang.org/x/net Go 1.26 stdlib incompatibility)
export GOTOOLCHAIN=go1.25.7

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

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
test: fmt vet ## Run tests.
	go test ./pkg/... -coverprofile cover.out

.PHONY: lint
lint: ## Run golangci-lint.
	golangci-lint run

##@ Build

.PHONY: build
build: fmt vet ## Build controller binary.
	go build -o bin/controller cmd/controller/main.go

.PHONY: run
run: fmt vet ## Run controller from your host.
	go run cmd/controller/main.go --env-config

.PHONY: docker-build
docker-build: ## Build docker image.
	docker build -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push docker image.
	docker push ${IMG}

##@ Deployment

.PHONY: install
install: ## Install Gateway API CRDs.
	kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.0.0/standard-install.yaml

.PHONY: uninstall
uninstall: ## Uninstall Gateway API CRDs.
	kubectl delete -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.0.0/standard-install.yaml

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
