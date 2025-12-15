FROM golang:1.22-alpine AS builder

WORKDIR /app

# Copy dependency files first for better layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY client ./client

# Build binary with optimizations
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /client ./client

FROM alpine:3.20

# Add metadata labels
LABEL maintainer="prr-playground"
LABEL description="HTTP client for load testing"
LABEL version="1.0"

# Create non-root user
RUN addgroup -g 1000 appuser && \
    adduser -D -u 1000 -G appuser appuser

# Copy binary from builder
COPY --from=builder /client /client

# Switch to non-root user
USER appuser

ENTRYPOINT ["/client"]

