# syntax=docker/dockerfile:1

# ── Stage 1: Build ──────────────────────────────────────────────────────────
# BUILDPLATFORM = host runner platform (always linux/amd64 on GitHub Actions)
# TARGETPLATFORM = the platform we're building FOR (amd64 or arm64)
FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS builder

ARG TARGETARCH
ARG TARGETOS=linux

RUN apk add --no-cache git ca-certificates

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Cross-compile natively — Go handles arm64 without any emulation
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w" -o /app/bot ./cmd/main.go

# ── Stage 2: Runtime ────────────────────────────────────────────────────────
FROM alpine:3.19

RUN apk add --no-cache git docker-cli ca-certificates tzdata

WORKDIR /app
COPY --from=builder /app/bot /app/bot

RUN mkdir -p /app/data /builds

EXPOSE 3000

HEALTHCHECK --interval=30s --timeout=10s --start-period=10s --retries=3 \
    CMD wget -qO- http://localhost:3000/api/health || exit 1

ENTRYPOINT ["/app/bot"]
