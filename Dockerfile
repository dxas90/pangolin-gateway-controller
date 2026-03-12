# syntax=docker/dockerfile:1

# ---------- Build stage ----------
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG BUILD_DATE=unknown
ARG VCS_REF=unknown

WORKDIR /workspace

# Minimal build deps
RUN apk add --no-cache git ca-certificates

# Dependency cache (fast rebuilds)
COPY go.mod go.sum ./
RUN go mod download

# Source code
COPY cmd/ cmd/
COPY pkg/ pkg/

# Build optimized, cross-platform, static binary
RUN CGO_ENABLED=0 \
    GOOS=${TARGETOS:-linux} \
    GOARCH=${TARGETARCH:-amd64} \
    go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION} -X main.buildDate=${BUILD_DATE}" \
      -o controller \
      cmd/controller/main.go

# ---------- Runtime stage ----------
FROM scratch

# TLS support
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Binary
COPY --from=builder /workspace/controller /controller

LABEL org.opencontainers.image.authors="Daniel Ramirez <dxas90@gmail.com>" \
    org.opencontainers.image.description="A container image for the Pangolin Gateway Controller." \
    org.opencontainers.image.licenses="MIT" \
    org.opencontainers.image.source="https://github.com/dxas90/pangolin-gateway-controller" \
    org.opencontainers.image.title="pangolin-gateway-controller Image"

# Non-root (same UID you used before)
USER 65532:65532

ENTRYPOINT ["/controller"]
