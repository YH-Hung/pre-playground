# Vector → Elasticsearch demo (Go server & client)

This example shows how to log per-request trace IDs, collect logs with Vector, and ship them to Elasticsearch 8 using Docker Compose.

## What's inside
- Go HTTP server (`server/`) writing JSON logs with `traceId` to `/var/log/app/app.log` with graceful shutdown, health/metrics endpoints, and configurable settings.
- Go client (`client/`) sending requests with a fresh `X-Trace-Id` header, featuring exponential backoff retry logic.
- Vector tailing the server log with native stateful aggregation and forwarding to Elasticsearch + stdout with retry configuration.
- Elasticsearch 8 single node with security disabled for simplicity.

## Features

### Server
- **Graceful Shutdown**: Handles SIGTERM/SIGINT signals, waits for in-flight requests, and flushes logs properly
- **Health & Metrics**: `/health` endpoint for health checks and `/metrics` endpoint with Prometheus-compatible metrics
- **Configuration**: Environment variable support for port, log path, and shutdown timeout
- **Observability**: Request count, error count, and average latency metrics

### Client
- **Retry Logic**: Exponential backoff retry for network errors and 5xx status codes (configurable max retries)
- **Configuration**: Environment variable support for all client parameters
- **Error Handling**: Distinguishes between retryable and non-retryable errors

### Vector
- **Robust Aggregation**: Native `reduce` transform provides built-in stateful aggregation by `traceId` with automatic memory management
- **Memory Management**: Automatic stale entry cleanup (30s timeout) and max buffer size limit (1000 entries)
- **Edge Case Handling**: Validates traceId, handles duplicate completion messages, and passes through entries without traceId
- **Retry Configuration**: Elasticsearch output includes retry settings for resilience
- **Fallback Output**: Console output ensures logs are visible even if Elasticsearch fails
- **Observability**: Built-in metrics and better debugging capabilities compared to Lua scripts

## Configuration

The project supports environment variables for configuration. You can create a `.env` file or set environment variables:

```bash
# Server Configuration
SERVER_PORT=8080
SERVER_LOG_PATH=/var/log/app/app.log
SERVER_SHUTDOWN_TIMEOUT=10s

# Client Configuration
CLIENT_TARGET_URL=http://server:8080/hello
CLIENT_COUNT=20
CLIENT_CONCURRENCY=3
CLIENT_INTERVAL=300ms
CLIENT_TIMEOUT=3s
CLIENT_MAX_RETRIES=3
```

## Run it (with explanation)
1) Build and start the stack (detached):
```sh
docker compose up --build -d
```
- `--build` forces rebuilding the Go server/client images so code + go.mod changes are picked up.
- `-d` runs containers in the background so you can use the same terminal for the next commands.

2) Check server health:
```sh
curl http://localhost:8080/health
```

3) View metrics:
```sh
curl http://localhost:8080/metrics
```

4) Generate traffic using the client container:
```sh
docker compose run --rm client \
  -target http://server:8080/hello \
  -count 20 \
  -concurrency 3 \
  -interval 300ms \
  -retries 3
```
- `docker compose run --rm client` starts a one-off client container then removes it when done, keeping your compose stack clean.
- `-target http://server:8080/hello` tells the client where to send requests; `server` resolves via the compose network to the Go server.
- `-count 20` sends 20 total requests so you get enough samples in Elasticsearch.
- `-concurrency 3` runs three workers in parallel to interleave trace IDs and show grouping across overlapping requests.
- `-interval 300ms` waits 300ms between requests per worker to avoid overwhelming the simple server while still generating multiple overlapping traces.
- `-retries 3` sets maximum retry attempts for failed requests (default: 3).

3) Watch Vector output (parsed logs):
```sh
docker compose logs -f vector
```
- `logs -f` tails the Vector container logs so you can see parsed JSON records and delivery status to Elasticsearch in real time.

4) Query Elasticsearch for recent logs (get 5 newest):
```sh
curl -s "http://localhost:9200/requests-*/_search" \
  -H 'Content-Type: application/json' \
  -d '{"size":5,"sort":[{"@timestamp":{"order":"desc"}}]}'
```
- `requests-*` matches the daily index name produced by Vector (`requests-%Y-%m-%d`).
- `size:5` limits to 5 docs to keep the output readable.
- `sort @timestamp desc` shows the most recent ingested entries first.

Filter by a specific trace (replace `<trace>` with a traceId from the logs):
```sh
curl -s "http://localhost:9200/requests-*/_search" \
  -H 'Content-Type: application/json' \
  -d '{"query":{"term":{"traceId.keyword":"<trace>"}}}'
```
- Each `traceId` should return exactly **one document** with all log lines combined.

5) Tear down and clean volumes:
```sh
docker compose down -v
```
- `down` stops and removes containers.
- `-v` also removes the named volume holding the server log, ensuring a clean slate for the next run.

## How Log Aggregation Works

This implementation combines multiple log lines from the same request (same `traceId`) into a **single Elasticsearch document** with a multi-line message field. Here's how it works:

### Pipeline Flow

```
Server Log File → Vector File Input → JSON Parser → Reduce Transform → Elasticsearch
```

### Step-by-Step Process

1. **File Input** (`sources.app_logs` in `vector.toml`):
   - Vector tails `/var/log/app/app.log` line by line
   - Each line is read and passed to the transform pipeline

2. **JSON Parsing** (`transforms.parse_json`):
   - Parses each log line as JSON using Vector Remap Language (VRL)
   - Extracts fields like `traceId`, `message`, `method`, `path`, `status`, `latencyMs`
   - Ensures `traceId` is a string (empty string if missing)
   - The parsed record continues to the next transform

3. **Stream Splitting** (`transforms.split_streams`):
   - Routes events into two streams:
     - `has_traceid`: Events with valid traceId (non-empty string) → for aggregation
     - `no_traceid`: Events without valid traceId → pass through directly

4. **Stateful Aggregation** (`transforms.aggregate_by_traceid` using `reduce` transform):
   - **Grouping**: Groups events by `traceId` field
   - **Memory Management**:
     - Automatically tracks and manages buffer state
     - Cleans up stale entries after 30 seconds (`flush_period_secs`)
     - Enforces maximum buffer size of 1000 entries (`max_events`)
   - **Merge Strategies**:
     - `message`: Concatenates all messages with `\n` separator (`concat_newline`)
     - `method`, `path`, `status`, `latencyMs`: Keeps latest values (`merge`)
   - **Conditional Flush**: When a log entry contains `"request completed"`:
     - The `ends_when` condition triggers
     - All buffered messages are combined with `\n` separator
     - A single combined record with merged fields is emitted
     - The buffer entry is automatically cleaned up
   - **Pass-through**: Entries without valid traceId bypass aggregation and go directly to output

5. **Output** (`sinks.elasticsearch` and `sinks.console`):
   - Combined records (one per `traceId`) and pass-through entries are sent to Elasticsearch
   - Each document contains all log lines from that request in a single `message` field
   - Includes retry configuration (5 retries with 1s wait) for resilience
   - Console sink provides fallback output for debugging

### Example Transformation

**Before aggregation** (multiple log lines):
```json
{"traceId": "abc", "message": "handler finished", "method": "GET", "path": "/hello", "status": 200, "latencyMs": 0}
{"traceId": "abc", "message": "request completed", "method": "GET", "path": "/hello", "status": 200, "latencyMs": 52}
```

**After aggregation** (single Elasticsearch document):
```json
{
  "traceId": "abc",
  "message": "handler finished\nrequest completed",
  "method": "GET",
  "path": "/hello",
  "status": 200,
  "latencyMs": 52
}
```

### Key Implementation Details

- **Stateful aggregation**: Vector's `reduce` transform maintains state automatically, grouping events by `traceId`
- **Stream routing**: Events are split into aggregation and pass-through streams based on traceId validity
- **Field merging**: Latest values for `method`, `path`, `status`, and `latencyMs` are kept using `merge` strategy
- **Message combination**: All messages are joined with `\n` separator using `concat_newline` merge strategy
- **Completion detection**: Uses VRL `contains!()` function to detect the "request completed" message pattern
- **Memory management**: Built-in handling of stale entries (30s timeout) and max buffer size (1000 entries)

### Configuration Files

- **`vector/vector.toml`**: Defines the Vector pipeline with file source, JSON parsing, reduce transform, and Elasticsearch/console sinks
- The configuration file is mounted into the Vector container via `docker-compose.yml`

## Architecture Diagram

```
┌─────────┐     ┌──────────┐     ┌─────────────┐     ┌──────────────┐
│ Client  │────▶│  Server  │────▶│  Log File   │────▶│    Vector    │
│ (retry) │     │(graceful)│     │  (buffered) │     │  (reduce)    │
└─────────┘     └──────────┘     └─────────────┘     └──────┬───────┘
                                                              │
                                                              ▼
                                                       ┌──────────────┐
                                                       │ Elasticsearch│
                                                       │  (retry cfg) │
                                                       └──────────────┘
```

## Troubleshooting

### Server won't start
- Check if the port is already in use: `lsof -i :8080`
- Verify log directory permissions: `ls -la /var/log/app`
- Check server logs: `docker compose logs server`

### Vector not processing logs
- Verify log file exists: `docker compose exec server ls -la /var/log/app/app.log`
- Check Vector logs: `docker compose logs vector`
- Verify Vector config is mounted: `docker compose exec vector ls -la /etc/vector/vector.toml`
- Check Vector configuration validity: `docker compose exec vector vector validate --config-dir /etc/vector`

### Elasticsearch connection issues
- Check Elasticsearch health: `curl http://localhost:9200/_cluster/health`
- Verify network connectivity: `docker compose exec vector ping elasticsearch`
- Check Vector retry logs for connection failures

### Memory issues with aggregation
- Vector's reduce transform includes automatic cleanup of stale entries (30s timeout)
- Maximum buffer size is limited to 1000 entries (`max_events` setting)
- Monitor Vector memory usage: `docker stats`
- Check Vector metrics: Vector exposes Prometheus metrics for monitoring aggregation state

### Client retry behavior
- Network errors are automatically retried with exponential backoff
- 5xx status codes are retried (500, 502, 503, etc.)
- 429 (Too Many Requests) is retried
- 4xx errors (except 429) are not retried
- Maximum retries can be configured via `-retries` flag or `CLIENT_MAX_RETRIES` env var

## Testing

Run unit tests:
```sh
# Server tests
go test ./server/... -v

# Client tests (requires network access for integration tests)
go test ./client/... -v
```

## Notes
- Server log file is volume-mounted so Vector and Elasticsearch see the same data.
- Elasticsearch security is disabled here for brevity; enable auth in real deployments.
- Vector's reduce transform buffer is in-memory by default; if Vector restarts, buffered entries may be lost (acceptable for this use case). Vector supports disk-backed state persistence for production use cases.
- Server runs as non-root user for security (uid 1000).
- Docker images are optimized with multi-stage builds and minimal base images.
- Graceful shutdown ensures logs are flushed before server exits.
- Vector provides better observability and maintainability compared to Lua scripts, with built-in metrics and easier configuration management.

