# syntax=docker/dockerfile:1

# ---------- Build stage ----------
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

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
      -ldflags="-s -w" \
      -o controller \
      cmd/controller/main.go

# ---------- Runtime stage ----------
FROM scratch

# TLS support
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Binary
COPY --from=builder /workspace/controller /controller

# Non-root (same UID you used before)
USER 65532:65532

ENTRYPOINT ["/controller"]
