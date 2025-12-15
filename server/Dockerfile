FROM golang:1.22-alpine AS builder

WORKDIR /app

# Copy dependency files first for better layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY server ./server

# Build binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /server ./server

FROM alpine:3.20

# Add metadata labels
LABEL maintainer="prr-playground"
LABEL description="HTTP server with trace logging"
LABEL version="1.0"

# Install wget for healthcheck
RUN apk add --no-cache wget

# Create non-root user
RUN addgroup -g 1000 appuser && \
    adduser -D -u 1000 -G appuser appuser

# Create log directory (permissions will be fixed by entrypoint if volume mounted)
RUN mkdir -p /var/log/app

# Copy binary from builder
COPY --from=builder /server /server

# Create entrypoint script to fix permissions
RUN echo '#!/bin/sh' > /entrypoint.sh && \
    echo 'chown -R appuser:appuser /var/log/app 2>/dev/null || true' >> /entrypoint.sh && \
    echo 'exec su-exec appuser /server' >> /entrypoint.sh && \
    chmod +x /entrypoint.sh

# Install su-exec for switching user
RUN apk add --no-cache su-exec

EXPOSE 8080

# Health check (can be overridden in docker-compose)
HEALTHCHECK --interval=10s --timeout=5s --start-period=5s --retries=3 \
    CMD wget --quiet --tries=1 --spider http://localhost:8080/health || exit 1

ENTRYPOINT ["/entrypoint.sh"]

