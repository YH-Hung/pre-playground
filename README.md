# Fluent Bit → Elasticsearch demo (Go server & client)

This example shows how to log per-request trace IDs, collect logs with Fluent Bit, and ship them to Elasticsearch 8 using Docker Compose.

## What's inside
- Go HTTP server (`server/`) writing JSON logs with `traceId` to `/var/log/app/app.log` with graceful shutdown, health/metrics endpoints, and configurable settings.
- Go client (`client/`) sending requests with a fresh `X-Trace-Id` header, featuring exponential backoff retry logic.
- Fluent Bit tailing the server log with memory-safe Lua aggregation and forwarding to Elasticsearch + stdout with retry configuration.
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

### Fluent Bit
- **Memory Leak Protection**: Lua script includes timestamp tracking, stale entry cleanup (30s timeout), and max buffer size limit (1000 entries) with LRU eviction
- **Edge Case Handling**: Validates traceId, handles duplicate completion messages, and includes nil checks
- **Retry Configuration**: Elasticsearch output includes retry settings for resilience
- **Fallback Output**: stdout output ensures logs are visible even if Elasticsearch fails

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

3) Watch Fluent Bit output (parsed logs):
```sh
docker compose logs -f fluent-bit
```
- `logs -f` tails the Fluent Bit container logs so you can see parsed JSON records and delivery status to Elasticsearch in real time.

4) Query Elasticsearch for recent logs (get 5 newest):
```sh
curl -s "http://localhost:9200/requests-*/_search" \
  -H 'Content-Type: application/json' \
  -d '{"size":5,"sort":[{"@timestamp":{"order":"desc"}}]}'
```
- `requests-*` matches the daily index name produced by Fluent Bit (`requests-%Y-%m-%d`).
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
Server Log File → Fluent Bit Tail Input → JSON Parser → Lua Aggregator → Elasticsearch
```

### Step-by-Step Process

1. **Input** (`[INPUT]` section in `fluent-bit.conf`):
   - Fluent Bit tails `/var/log/app/app.log` line by line
   - Each line is tagged as `app` and passed to the filter chain

2. **JSON Parsing** (`[FILTER]` parser):
   - Parses each log line as JSON
   - Extracts fields like `traceId`, `message`, `method`, `path`, `status`, `latencyMs`
   - The parsed record continues to the next filter

3. **Lua Aggregation** (`[FILTER]` lua + `aggregate.lua`):
   - **Buffering**: Maintains an in-memory buffer keyed by `traceId` with memory leak protection
   - **Memory Management**:
     - Tracks `last_seen` timestamp for each buffer entry
     - Periodically cleans up stale entries (older than 30 seconds)
     - Enforces maximum buffer size (1000 entries) with LRU eviction
     - Handles duplicate completion messages gracefully
   - **For each incoming log entry**:
     - Validates traceId format (non-empty string)
     - If `traceId` exists in buffer: appends the `message` to the buffer entry's message array
     - If new `traceId`: creates a new buffer entry (evicting oldest if at capacity)
     - Updates other fields (method, path, status, latencyMs) with latest values
     - Updates `last_seen` timestamp and LRU order
   - **Suppression**: Returns `-1` to drop intermediate entries (they're buffered, not sent yet)
   - **Flush trigger**: When a log entry contains `"request completed"`:
     - Combines all buffered messages with `\n` separator: `message1\nmessage2\n...`
     - Creates a single combined record with merged fields
     - Returns `1` to pass the combined record to output
     - Removes the entry from buffer and LRU tracking

4. **Output** (`[OUTPUT]` es):
   - Only the combined records (one per `traceId`) are sent to Elasticsearch
   - Each document contains all log lines from that request in a single `message` field
   - Includes retry configuration (5 retries with 1s wait) for resilience
   - Falls back to stdout output if Elasticsearch is unavailable

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

- **Buffer storage**: The Lua script uses a global `buffer` table to store entries by `traceId`
- **Return codes**: 
  - `-1`: Drop/suppress the record (intermediate entries)
  - `1`: Pass the record through (combined entry or entries without traceId)
- **Field merging**: Latest values for `status` and `latencyMs` are kept (from the "request completed" entry)
- **Message combination**: All messages are joined with `\n` to create a multi-line text field
- **Completion detection**: Uses `string.find()` to detect the "request completed" message pattern

### Configuration Files

- **`fluent-bit/fluent-bit.conf`**: Defines the filter pipeline with Lua filter
- **`fluent-bit/aggregate.lua`**: Contains the aggregation logic
- Both files are mounted into the Fluent Bit container via `docker-compose.yml`

## Architecture Diagram

```
┌─────────┐     ┌──────────┐     ┌─────────────┐     ┌──────────────┐
│ Client  │────▶│  Server  │────▶│  Log File   │────▶│  Fluent Bit  │
│ (retry) │     │(graceful)│     │  (buffered) │     │  (fixed Lua) │
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

### Fluent Bit not processing logs
- Verify log file exists: `docker compose exec server ls -la /var/log/app/app.log`
- Check Fluent Bit logs: `docker compose logs fluent-bit`
- Verify Lua script is mounted: `docker compose exec fluent-bit ls -la /fluent-bit/etc/aggregate.lua`

### Elasticsearch connection issues
- Check Elasticsearch health: `curl http://localhost:9200/_cluster/health`
- Verify network connectivity: `docker compose exec fluent-bit ping elasticsearch`
- Check Fluent Bit retry logs for connection failures

### Memory issues with Lua aggregation
- The Lua script now includes automatic cleanup of stale entries (30s timeout)
- Maximum buffer size is limited to 1000 entries with LRU eviction
- Monitor Fluent Bit memory usage: `docker stats`

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
- Server log file is volume-mounted so Fluent Bit and Elasticsearch see the same data.
- Elasticsearch security is disabled here for brevity; enable auth in real deployments.
- The Lua buffer is in-memory only; if Fluent Bit restarts, buffered entries may be lost (acceptable for this use case).
- Server runs as non-root user for security (uid 1000).
- Docker images are optimized with multi-stage builds and minimal base images.
- Graceful shutdown ensures logs are flushed before server exits.

