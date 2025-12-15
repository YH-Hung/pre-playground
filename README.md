# Fluent Bit → Elasticsearch demo (Go server & client)

This example shows how to log per-request trace IDs, collect logs with Fluent Bit, and ship them to Elasticsearch 8 using Docker Compose.

## What's inside
- Go HTTP server (`server/`) writing JSON logs with `traceId` to `/var/log/app/app.log`.
- Go client (`client/`) sending requests with a fresh `X-Trace-Id` header.
- Fluent Bit tailing the server log and forwarding to Elasticsearch + stdout.
- Elasticsearch 8 single node with security disabled for simplicity.

## Run it (with explanation)
1) Build and start the stack (detached):
```sh
docker compose up --build -d
```
- `--build` forces rebuilding the Go server/client images so code + go.mod changes are picked up.
- `-d` runs containers in the background so you can use the same terminal for the next commands.

2) Generate traffic using the client container:
```sh
docker compose run --rm client \
  -target http://server:8080/hello \
  -count 20 \
  -concurrency 3 \
  -interval 300ms
```
- `docker compose run --rm client` starts a one-off client container then removes it when done, keeping your compose stack clean.
- `-target http://server:8080/hello` tells the client where to send requests; `server` resolves via the compose network to the Go server.
- `-count 20` sends 20 total requests so you get enough samples in Elasticsearch.
- `-concurrency 3` runs three workers in parallel to interleave trace IDs and show grouping across overlapping requests.
- `-interval 300ms` waits 300ms between requests per worker to avoid overwhelming the simple server while still generating multiple overlapping traces.

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
- `traceId.keyword` matches the exact trace ID string; this returns all lines (middleware + handler) for that request to demonstrate grouping.

5) Tear down and clean volumes:
```sh
docker compose down -v
```
- `down` stops and removes containers.
- `-v` also removes the named volume holding the server log, ensuring a clean slate for the next run.

## Notes
- Server log file is volume-mounted so Fluent Bit and Elasticsearch see the same data.
- Elasticsearch security is disabled here for brevity; enable auth in real deployments.

