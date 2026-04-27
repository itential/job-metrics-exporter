# job-metrics-exporter

[![License](https://img.shields.io/badge/License-GPL--3.0-blue.svg)](LICENSE)

A [Prometheus](https://prometheus.io) exporter for [Itential Automation Platform (IAP)](https://www.itential.com) Workflow Engine metrics. It connects to a MongoDB replica set, tracks job and task lifecycle events via MongoDB change streams, and optionally runs background aggregation queries to produce status count snapshots — all exposed via an HTTP or HTTPS endpoint.

---

## Table of Contents

- [Overview](#overview)
- [Metrics](#metrics)
- [Requirements](#requirements)
- [MongoDB Setup](#mongodb-setup)
- [Installation](#installation)
  - [Option 1 — Build and Install](#option-1--build-and-install)
  - [Option 2 — Download a Pre-Built Binary](#option-2--download-a-pre-built-binary)
  - [Option 3 — Container / Kubernetes](#option-3--container--kubernetes)
- [Configuration](#configuration)
- [Running as a systemd Service](#running-as-a-systemd-service)
- [TLS](#tls)
- [Prometheus Configuration](#prometheus-configuration)
- [Grafana](#grafana)
- [Building from Source](#building-from-source)
- [Architecture](#architecture)
- [Troubleshooting](#troubleshooting)
- [Contributing](#contributing)
- [License](#license)

---

## Overview

`itential-job-metrics-exporter` provides two complementary metric collection mechanisms that can be enabled independently:

**Change Stream (real-time)**
Connects to the MongoDB change stream on the `jobs` and `tasks` collections. Increments ever-increasing counters each time a job or task is inserted or transitions to `complete`, `error`, or `canceled`. Also maintains real-time status gauges via ±1 deltas, seeded by a one-time bootstrap poll on startup. Use Prometheus `rate()` or `increase()` to graph activity over time in Grafana.

**Background Polling (status snapshots)**
Runs aggregation queries against `jobs` and `tasks` on a configurable interval, producing a current snapshot of document counts grouped by status. Results are served from an in-memory cache so Prometheus scrapes are never blocked on MongoDB. Use this when MongoDB is not running as a replica set.

Key properties:

- **Non-blocking scrapes** — polling results are cached in memory; `/metrics` always responds immediately.
- **Secondary preferred** — all MongoDB reads use `SecondaryPreferred`, keeping load off the primary.
- **Index-safe queries** — every aggregation hints a specific index and will error rather than fall back to a collection scan.
- **Graceful reconnection** — if the change stream drops (network blip, election), the watcher reconnects automatically with exponential backoff (1s → 60s) using a resume token so no events are lost.

---

## Metrics

All metrics are prefixed with `itential_`.

### Change Stream Counters

These are ever-increasing counters. Use `rate()` or `increase()` in Grafana to see activity over time.

| Metric | Type | Labels | Description |
|---|---|---|---|
| `itential_job_start` | Counter | — | Total jobs inserted (started) |
| `itential_job_complete` | Counter | — | Total jobs that transitioned to status `complete` |
| `itential_job_error` | Counter | — | Total jobs that transitioned to status `error` |
| `itential_job_cancel` | Counter | — | Total jobs that transitioned to status `canceled` |
| `itential_task_start` | Counter | `server_id` | Total tasks inserted, labelled by the worker that ran them |
| `itential_task_complete` | Counter | `server_id` | Total tasks that transitioned to status `complete`, labelled by worker |
| `itential_task_error` | Counter | `server_id` | Total tasks that transitioned to status `error`, labelled by worker |
| `itential_task_cancel` | Counter | `server_id` | Total tasks that transitioned to status `canceled`, labelled by worker |

The `server_id` label is sourced from `metrics.server_id` on the task document. Requires `change_stream.enabled: true`.

### Status Gauges

Point-in-time snapshots of document counts per status. Updated in real-time when `change_stream.enabled: true`, or refreshed on the configured polling interval when `polling.enabled: true`.

| Metric | Type | Labels | Description |
|---|---|---|---|
| `itential_job_status_total` | Gauge | `status` | Current number of jobs per status |
| `itential_task_status_total` | Gauge | `status` | Current number of tasks per status |

Status values are discovered dynamically from MongoDB — no configuration is needed when new status values appear.

---

## Requirements

### Runtime

- Linux: RHEL 8/9, Rocky 8/9, Amazon Linux 2023, Oracle Linux 8/9 (amd64 or arm64)
- MongoDB 4.2 or later, running as a **replica set** (required for change streams)
- A dedicated MongoDB user with `read` access to the `itential` database (see [MongoDB Setup](#mongodb-setup))

### Build

- Go 1.22 or later
- GNU Make

---

## MongoDB Setup

### Create a Read-Only User

```javascript
use admin
db.createUser({
  user: "prometheus",
  pwd: "<password>",
  roles: [
    { role: "read", db: "itential" }
  ]
})
```

The `read` role grants `changeStream` access on MongoDB 4.0+. No additional grants are needed for change streams.

### Index Requirements

The background polling queries (and change stream bootstrap) require the following indexes.

| Query | Collection | Index Name | Index Keys |
|---|---|---|---|
| `JobsByStatus` | `jobs` | `itential_status` | `{status: 1, _id: 1}` |
| `TasksByStatus` | `tasks` | `itential_job_metrics_exporter_task_status_server` | `{status: 1, metrics.server_id: 1}` |

All aggregations set an explicit index hint and will return an error rather than fall back to a collection scan if the expected index is missing.

Run these against the `itential` database (or whichever database is configured via `ITENTIAL_JOB_METRIC_MONGO_DATABASE`):

```javascript
// jobs collection — covered index scan on status, no document fetch required
// Note: itential_status is typically created by the Itential platform application
// and may already exist in your environment.
db.jobs.createIndex(
  { status: 1, _id: 1 },
  { name: "itential_status", background: true }
)

// tasks collection — used by all task status, per-server, and duration queries
db.tasks.createIndex(
  { status: 1, "metrics.server_id": 1 },
  { name: "itential_job_metrics_exporter_task_status_server", background: true }
)
```

---

## Installation

### Option 1 — Build and Install

```bash
git clone https://github.com/itential/job-metrics-exporter.git
cd job-metrics-exporter

# Cross-compile for Linux x86_64
make release-linux-amd64

# Install binary, config, and systemd unit (requires root)
sudo make install
```

`make install` places:
- Binary at `/usr/local/bin/itential-job-metrics-exporter`
- Starter config at `/etc/itential-job-metrics-exporter/config.yaml`
- systemd unit at `/etc/systemd/system/itential-job-metrics-exporter.service`

### Create the System User

```bash
sudo useradd --system --no-create-home --shell /sbin/nologin prometheus
sudo chown -R prometheus:prometheus /etc/itential-job-metrics-exporter
```

### Option 2 — Download a Pre-Built Binary

Pre-built binaries for amd64 and arm64 are available on the [Releases](https://github.com/itential/job-metrics-exporter/releases) page.

```bash
# Download the latest Linux amd64 binary
curl -Lo itential-job-metrics-exporter \
  https://github.com/itential/job-metrics-exporter/releases/latest/download/itential-job-metrics-exporter-linux-amd64

# Install it
sudo install -Dm755 itential-job-metrics-exporter /usr/local/bin/itential-job-metrics-exporter
```

Then follow the [Create the System User](#create-the-system-user) and [Running as a systemd Service](#running-as-a-systemd-service) steps above, placing the config file manually at `/etc/itential-job-metrics-exporter/config.yaml`.

### Option 3 — Container / Kubernetes

The exporter can be configured entirely via environment variables — no config file required. This makes it well suited for container and Kubernetes deployments.

```bash
docker run --rm \
  -e ITENTIAL_JOB_METRIC_MONGO_URI="mongodb://prometheus:<password>@<hostname>:27017/itential?replicaSet=rs0&authSource=admin" \
  -e ITENTIAL_JOB_METRIC_CHANGE_STREAM_ENABLED=true \
  -p 9477:9477 \
  ghcr.io/itential/job-metrics-exporter:latest
```

For Kubernetes, inject credentials via a `Secret` and reference them as environment variables in your `Deployment`:

```yaml
env:
  - name: ITENTIAL_JOB_METRIC_MONGO_URI
    valueFrom:
      secretKeyRef:
        name: job-metrics-exporter
        key: mongo-uri
  - name: ITENTIAL_JOB_METRIC_CHANGE_STREAM_ENABLED
    value: "true"
```

See the [Configuration](#configuration) section for the full list of environment variables.

---

## Configuration

Configuration precedence (highest to lowest):

1. CLI flags
2. Environment variables (`ITENTIAL_JOB_METRIC_*`)
3. YAML config file
4. Built-in defaults

### Command-Line Flags

| Flag | Description |
|---|---|
| `--config` | Path to the YAML configuration file |
| `--database` | Override the MongoDB database name |
| `--listen-address` | Override the listen address (e.g. `:9477`) |
| `--version` | Print the version and exit |

### YAML Configuration

```yaml
mongo:
  # Option A: Full connection string (recommended for replica sets).
  uri: "mongodb://prometheus:<password>@<hostname-1>:27017,<hostname-2>:27017,<hostname-3>:27017/itential?replicaSet=rs0&authSource=admin"

  # Option B: Individual parameters (used when uri is not set).
  # host: "localhost"
  # port: 27017
  # username: "prometheus"
  # password: "<password>"
  # database: "itential"
  # auth_source: "admin"

  tls:
    enabled: false
    ca_file: "/etc/itential-job-metrics-exporter/ca.pem"
    insecure_skip_verify: false   # never true in production

exporter:
  listen_address: ":9477"
  metrics_path: "/metrics"
  # Timeout for the one-time bootstrap poll run at startup when change_stream
  # is enabled. Should be long enough for a full status scan of your collections.
  slow_query_timeout: "4m"
  tls:
    enabled: false
    cert_file: "/etc/itential-job-metrics-exporter/server.crt"
    key_file:  "/etc/itential-job-metrics-exporter/server.key"

log:
  level: "info"    # debug | info | warn | error
  format: "json"   # json | text

# Real-time change stream counters.
change_stream:
  enabled: true
  initial_load: true
  initial_load_timeout: "2m"

# Background aggregation queries for status count snapshots.
# Use this when change_stream is not available (MongoDB not on a replica set).
# If change_stream is enabled, polling is not started.
polling:
  enabled: false
  interval: "60s"
  query_timeout: "55s"   # must be less than interval
```

### Environment Variables

| Variable | Description |
|---|---|
| `ITENTIAL_JOB_METRIC_MONGO_URI` | Full MongoDB connection string |
| `ITENTIAL_JOB_METRIC_MONGO_HOST` | MongoDB host |
| `ITENTIAL_JOB_METRIC_MONGO_PORT` | MongoDB port |
| `ITENTIAL_JOB_METRIC_MONGO_USERNAME` | MongoDB username |
| `ITENTIAL_JOB_METRIC_MONGO_PASSWORD` | MongoDB password |
| `ITENTIAL_JOB_METRIC_MONGO_DATABASE` | MongoDB database name |
| `ITENTIAL_JOB_METRIC_MONGO_AUTH_SOURCE` | MongoDB auth source database |
| `ITENTIAL_JOB_METRIC_MONGO_TLS_ENABLED` | Enable MongoDB TLS (`true`/`false`) |
| `ITENTIAL_JOB_METRIC_MONGO_TLS_CA_FILE` | Path to CA certificate file |
| `ITENTIAL_JOB_METRIC_MONGO_TLS_INSECURE` | Skip TLS verification (`true`/`false`) |
| `ITENTIAL_JOB_METRIC_LISTEN_ADDRESS` | Exporter listen address |
| `ITENTIAL_JOB_METRIC_METRICS_PATH` | Metrics endpoint path |
| `ITENTIAL_JOB_METRIC_SLOW_QUERY_TIMEOUT` | Bootstrap query timeout for change stream startup (e.g. `4m`) |
| `ITENTIAL_JOB_METRIC_TLS_ENABLED` | Enable TLS on exporter endpoint (`true`/`false`) |
| `ITENTIAL_JOB_METRIC_TLS_CERT_FILE` | Path to TLS certificate |
| `ITENTIAL_JOB_METRIC_TLS_KEY_FILE` | Path to TLS key |
| `ITENTIAL_JOB_METRIC_LOG_LEVEL` | Log level (`debug`/`info`/`warn`/`error`) |
| `ITENTIAL_JOB_METRIC_LOG_FORMAT` | Log format (`json`/`text`) |
| `ITENTIAL_JOB_METRIC_CHANGE_STREAM_ENABLED` | Enable change stream watcher (`true`/`false`) |
| `ITENTIAL_JOB_METRIC_CHANGE_STREAM_INITIAL_LOAD` | Load active doc states on startup (`true`/`false`) |
| `ITENTIAL_JOB_METRIC_CHANGE_STREAM_INITIAL_LOAD_TIMEOUT` | Deadline for initial load queries (e.g. `2m`) |
| `ITENTIAL_JOB_METRIC_POLLING_ENABLED` | Enable background polling (`true`/`false`) |
| `ITENTIAL_JOB_METRIC_POLLING_INTERVAL` | Polling interval (e.g. `60s`) |
| `ITENTIAL_JOB_METRIC_POLLING_QUERY_TIMEOUT` | Per-query timeout (e.g. `55s`) |

---

## Running as a systemd Service

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now itential-job-metrics-exporter
sudo systemctl status itential-job-metrics-exporter
```

### Useful Commands

```bash
# Live logs
sudo journalctl -u itential-job-metrics-exporter -f

# Restart after config change
sudo systemctl restart itential-job-metrics-exporter

# Check the metrics endpoint
curl http://localhost:9477/metrics
curl http://localhost:9477/healthz
```

### Using an EnvironmentFile for Secrets

```bash
sudo tee /etc/itential-job-metrics-exporter/env <<EOF
ITENTIAL_JOB_METRIC_MONGO_PASSWORD=your-secret-password
EOF
sudo chmod 600 /etc/itential-job-metrics-exporter/env
sudo chown prometheus:prometheus /etc/itential-job-metrics-exporter/env
```

Uncomment the `EnvironmentFile` line in the systemd unit, then `systemctl daemon-reload && systemctl restart itential-job-metrics-exporter`.

---

## TLS

### MongoDB TLS

```yaml
mongo:
  tls:
    enabled: true
    ca_file: "/etc/itential-job-metrics-exporter/mongo-ca.pem"
```

### Exporter Endpoint TLS

```yaml
exporter:
  tls:
    enabled: true
    cert_file: "/etc/itential-job-metrics-exporter/server.crt"
    key_file:  "/etc/itential-job-metrics-exporter/server.key"
```

---

## Prometheus Configuration

### Basic (HTTP)

```yaml
scrape_configs:
  - job_name: itential_job_metric
    scrape_interval: 60s
    scrape_timeout: 10s
    static_configs:
      - targets:
          - "<hostname>:9477"
```

### With TLS (HTTPS)

```yaml
scrape_configs:
  - job_name: itential_job_metric
    scrape_interval: 60s
    scrape_timeout: 10s
    scheme: https
    tls_config:
      ca_file: /etc/prometheus/ca.pem
    static_configs:
      - targets:
          - "<hostname>:9477"
```

---

## Grafana

### Change Stream Panels

**Job throughput (rate)**
```promql
rate(itential_job_complete[5m])
```

**Task completions per worker (stacked bar)**
```promql
rate(itential_task_complete[5m])
```
Group by `server_id` to see per-worker throughput.

**Job starts vs completions**
```promql
rate(itential_job_start[5m])
rate(itential_job_complete[5m])
```

**Job and task cancellations over time**
```promql
rate(itential_job_cancel[5m])
increase(itential_task_cancel{server_id=~"$server_id"}[5m])
```

> Use `increase()` rather than `rate()` for cancellations — they are typically infrequent events and `increase()` returns a count over the window rather than a per-second rate, which is easier to read.

### Status Gauge Panels

**Current jobs by status (table or bar chart)**
```promql
itential_job_status_total
```

**Current tasks by status**
```promql
itential_task_status_total
```

**Error tasks growing (alert candidate)**
```promql
deriv(itential_task_status_total{status="error"}[10m])
```

### Alerting Examples

```yaml
- alert: ItentialJobMetricErrorTasksGrowing
  expr: deriv(itential_task_status_total{status="error"}[10m]) > 0.5
  for: 5m
  annotations:
    summary: "Itential job metric error task count is increasing"

- alert: ItentialJobMetricJobErrors
  expr: rate(itential_job_error[5m]) > 0
  for: 2m
  annotations:
    summary: "IAP jobs are transitioning to error status"

- alert: ItentialJobMetricTaskErrors
  expr: rate(itential_task_error[5m]) > 0
  for: 2m
  labels:
    severity: warning
  annotations:
    summary: "IAP tasks are erroring on worker {{ $labels.server_id }}"

- alert: ItentialJobMetricNoJobActivity
  expr: rate(itential_job_start[15m]) == 0
  for: 15m
  annotations:
    summary: "No new IAP jobs have started in 15 minutes"

- alert: ItentialJobMetricJobCancellations
  expr: rate(itential_job_cancel[5m]) > 0
  for: 2m
  annotations:
    summary: "IAP jobs are being canceled"

- alert: ItentialJobMetricTaskCancellations
  expr: rate(itential_task_cancel[5m]) > 0
  for: 2m
  labels:
    severity: warning
  annotations:
    summary: "IAP tasks are being canceled on worker {{ $labels.server_id }}"
```

---

## Building from Source

```bash
make build                  # Build for current OS/arch → dist/
make test                   # Run all tests with race detector
make lint                   # Run golangci-lint
make release-linux-amd64    # Cross-compile for Linux x86_64
make release-all            # Build all platforms
```

### Testing

The test suite requires no external services. All tests are pure Go unit tests:

```bash
# Run all tests with race detector (same as make test)
go test -v -race ./internal/...

# Run a single package
go test -v -race ./internal/collector/...
go test -v -race ./internal/config/...
go test -v -race ./internal/watcher/...

# Run a single test function
go test ./internal/... -run TestFunctionName
```

| Package | What is tested |
|---|---|
| `internal/config` | YAML loading, env var overrides and precedence, all startup validation rules |
| `internal/collector` | Counter and gauge correctness, zero-defaulting for unseen servers, concurrent write safety |
| `internal/watcher` | Every job and task event type (insert, update, replace, cancel) using raw BSON payloads and a mock `EventRecorder` |

---

## Architecture

```
cmd/exporter/main.go          Entry point. Wires config, MongoDB client,
                               collector, change stream watcher, background
                               poller, and HTTP server.

internal/config/              Config loading with precedence:
  config.go                   CLI flags > env vars > YAML file > defaults.

internal/mongoclient/         MongoDB client. Handles URI vs individual
  client.go                   params, TLS, auth, SecondaryPreferred read
                               preference, and connectivity verification.

internal/collector/           Prometheus Collector. Holds in-memory counters
  collector.go                (change stream) and status count caches
                               (polling/bootstrap). Collect() always reads
                               from memory.

internal/watcher/             Change stream consumer. Watches jobs and tasks
  watcher.go                  collections. Reconnects with exponential backoff
                               on failure, resume token on reconnect.

internal/queries/             MongoDB aggregation pipelines used by the
  queries.go                  background poller and change stream bootstrap.
                               All queries hint a named index and target
                               SecondaryPreferred.
```

### Data Flow

```
MongoDB change stream ──► watcher.handleJobEvent/handleTaskEvent
                                │
                                ▼
                       collector.RecordJobStart()
                       collector.ApplyJobStatusDelta(status, ±1)
                                │
                                ▼
                       in-memory counters (int64) + status gauges (map)

Background poller ──► queries.JobsByStatus / TasksByStatus
  (every interval)             │
                               ▼
                      collector.SetJobStatusCounts()
                      collector.SetTaskStatusCounts()
                               │
                               ▼
                      in-memory maps (map[string]int64)

Prometheus scrape ──► collector.Collect()
                               │
                               ▼
                      reads counters + maps atomically
                      emits metrics (never blocks on Mongo)
```

---

## Troubleshooting

### No change stream metrics appearing

1. Confirm `change_stream.enabled: true` in config.
2. Confirm `log.level: "debug"` and look for `"watching jobs change stream"` in logs.
3. Confirm MongoDB is a replica set — change streams require oplog access.
4. Confirm the MongoDB user has the `read` role on `itential` (which includes `changeStream` on MongoDB 4.0+).

### Change stream watcher keeps reconnecting

Check logs for the `"change stream error, will retry"` message and the `err` field. Common causes: network interruption, MongoDB election, or the resume token expiring (oplog rolled over during a long outage). The exporter will recover automatically.

### Polling queries failing (`context deadline exceeded`)

The aggregation is taking longer than `polling.query_timeout`. Options:
1. Increase `polling.query_timeout` and `polling.interval` (no rebuild required).
2. Verify the required indexes exist on the collection.
3. Check replica set replication lag — a lagging secondary may be slow.

### `polling.query_timeout must be less than polling.interval`

Enforced at startup. Ensure `query_timeout` is strictly less than `interval`. Example: `interval: "60s"`, `query_timeout: "55s"`.

### MongoDB connection succeeds but reads fail on secondary

Verify the user has the `read` role and it has replicated:

```javascript
// Run on each secondary
use itential
db.auth("prometheus", "<password>")
db.tasks.countDocuments({}, { limit: 1 })
```

### Exporter starts but `itential_job_status_total` never appears

- **Change stream mode**: look for `"bootstrapping status gauges"` in the logs. The metric is seeded by the bootstrap poll — if it's missing, the bootstrap query likely failed (check for errors and verify the required indexes exist).
- **Polling mode**: confirm `polling.enabled: true` and look for `"job status counts updated"` in debug logs. The metric won't appear until the first successful poll completes.

---

## Contributing

Contributions are welcome! Please read our [Contributing Guide](CONTRIBUTING.md) to get started.

Before contributing, you'll need to sign our [Contributor License Agreement](CLA.md).

- **Bug Reports**: [Open an issue](https://github.com/itential/job-metrics-exporter/issues/new)
- **Questions**: [Start a discussion](https://github.com/itential/job-metrics-exporter/discussions)
- **Maintainer**: [@Nick-Andreano](https://github.com/Nick-Andreano)

## License

This project is licensed under the GNU General Public License v3.0 - see the [LICENSE](LICENSE) file for details.

---

<p align="center">
  Made with ❤️ by the <a href="https://github.com/itential">Itential</a> community
</p>
