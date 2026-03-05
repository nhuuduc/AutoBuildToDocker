# syntax=docker/dockerfile:1

# ── Stage 1: Build ──────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /app/bot ./cmd/main.go

# ── Stage 2: Runtime ────────────────────────────────────────────────────────
FROM alpine:3.19

# Need git for cloning repos and docker CLI for building images
RUN apk add --no-cache git docker-cli ca-certificates tzdata

WORKDIR /app
COPY --from=builder /app/bot /app/bot

# Data and build directories
RUN mkdir -p /app/data /builds

EXPOSE 3000

HEALTHCHECK --interval=30s --timeout=10s --start-period=10s --retries=3 \
    CMD wget -qO- http://localhost:3000/api/health || exit 1

ENTRYPOINT ["/app/bot"]
