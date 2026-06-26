# CLAUDE.md — job-metrics-exporter

## What This Is

`itential-job-metrics-exporter` is a Prometheus exporter for [Itential Automation Platform (IAP)](https://www.itential.com). It connects to a MongoDB replica set (the backing store for IAP's Workflow Engine), watches the `jobs` and `tasks` collections, and exposes job/task lifecycle and status count metrics via an HTTP or HTTPS endpoint scraped by Prometheus.

The binary is a single statically-linked Go executable. It is distributed as a pre-built linux/amd64 and linux/arm64 binary and as a container image published to `ghcr.io/itential/job-metrics-exporter`. It is intended to run as a systemd service or in Kubernetes.

---

## Repository Layout

```
cmd/exporter/main.go          Entry point and wiring
internal/collector/           Prometheus Collector (all in-memory state)
internal/config/              Config loading: YAML + env vars + CLI flags
internal/mongoclient/         MongoDB client construction
internal/queries/             MongoDB aggregation pipelines
internal/watcher/             MongoDB change stream consumer
.githooks/                    pre-commit and commit-msg hook scripts
.github/workflows/            CI: release.yml, pr-compliance.yml
Dockerfile                    Multi-stage container image build
Makefile                      Build, test, lint, install targets
config.example.yaml           Annotated reference config
itential-job-metrics-exporter.service.sample  systemd unit template
```

---

## Architecture

There are two complementary metric collection modes, configurable independently.

### Change Stream Mode (`change_stream.enabled: true`)

Requires MongoDB to be a **replica set** (oplog access for change streams).

1. On startup: runs one bootstrap poll (`queries.JobsByStatus` + `queries.TasksByStatus`) to seed the initial status gauge values.
2. Opens change streams on the `jobs` and `tasks` collections via `watcher.Watcher`.
3. Every insert/update/replace event calls into `collector.EventRecorder` to increment lifecycle counters and applies ±1 deltas to status gauges.
4. On failure: reconnects automatically using exponential backoff (1s → 60s cap) with resume tokens so no events are lost.
5. On reconnect: calls `collector.HealthRecorder.RecordWatcherReconnect(collection)` to increment the reconnect metric.

### Polling Mode (`polling.enabled: true`)

Does **not** require a replica set. Runs `queries.JobsByStatus` + `queries.TasksByStatus` on a configurable interval. Results replace the collector's in-memory status gauge cache. Status counter metrics (job_start, job_complete, etc.) are not populated in polling-only mode — those require the change stream.

If `change_stream.enabled` is true, polling is **not** started even if `polling.enabled` is also true.

### Scrape Path

Every Prometheus scrape hits `collector.Collect()`. It:
1. Pings MongoDB with a 3-second timeout → emits `itential_up` (1 or 0).
2. Takes the mutex, snapshots all counters and maps, releases the mutex.
3. Emits all metrics from the snapshot.
4. Emits `itential_scrape_duration_seconds` (total time including the ping).

**Scrapes never block on MongoDB I/O** beyond the 3-second ping.

---

## Packages

### `cmd/exporter/main.go`

Entry point. Wires everything together:
- Parses CLI flags: `--config`, `--database`, `--listen-address`, `--version`
- Loads config via `config.Load()`
- Constructs MongoDB client via `mongoclient.New()`
- Constructs `collector.New()` with a ping closure over the MongoDB client
- If `change_stream.enabled`: runs bootstrap poll, then starts `watcher.New(...).Start(ctx)` in a goroutine
- If `polling.enabled` (and change stream is off): starts `runPoller()` in a goroutine
- Registers the collector in a private Prometheus registry (not the default global one)
- Serves `/metrics`, `/healthz`, and `/` (HTML landing page)
- Handles SIGINT/SIGTERM for graceful shutdown

### `internal/config`

Config structs, YAML loading, env var overlay, and validation. Precedence (highest to lowest):

1. CLI flags (`--database`, `--listen-address`)
2. Environment variables (`ITENTIAL_JOB_METRIC_*`)
3. YAML config file (if `--config` is given)
4. Built-in defaults

Key defaults: listen `:9477`, metrics path `/metrics`, database `itential`, host `localhost:27017`, change stream and polling both disabled, log `info`/`json`.

Validation rules enforced at startup:
- TLS cert and key required if exporter TLS is enabled
- MongoDB CA file existence is stat-checked if TLS is enabled
- `query_timeout < cache_ttl` and `slow_query_timeout < slow_cache_ttl`
- `polling.query_timeout < polling.interval`

### `internal/mongoclient`

Builds a `*mongo.Client`. Key behaviors:
- If `mongo.uri` is set, it is used verbatim; individual host/port/auth fields are ignored (TLS still applies on top)
- Read preference is always `SecondaryPreferred` — no queries ever hit the primary
- Pings at construction time to verify connectivity; returns error if MongoDB is unreachable
- Timeouts: 15s connect, 15s server selection, 60s socket

### `internal/collector`

The Prometheus `Collector`. Central in-memory state store, protected by a single `sync.Mutex`.

**Interfaces defined here:**

```go
// EventRecorder — called by the watcher on each lifecycle event
type EventRecorder interface {
    RecordJobStart(); RecordJobComplete(); RecordJobError(); RecordJobCancel()
    RecordTaskStart(serverID string); RecordTaskComplete(serverID string)
    RecordTaskError(serverID string); RecordTaskCancel(serverID string)
    ApplyJobStatusDelta(status string, delta int64)
    ApplyTaskStatusDelta(status string, delta int64)
}

// HealthRecorder — called by the watcher on reconnect
type HealthRecorder interface {
    RecordWatcherReconnect(collection string)
}
```

**State fields:**
- `jobStarts/Completes/Errors/Cancels int64` — scalar lifecycle counters
- `taskStarts/Completes/Errors/Cancels map[string]int64` — keyed by `server_id`
- `jobStatusCounts/taskStatusCounts map[string]int64` — keyed by status string; replaced by polling or delta-updated by change stream
- `watcherReconnects map[string]int64` — keyed by collection; pre-populated `{"jobs":0,"tasks":0}`
- `pollErrors map[string]int64` — keyed by query name; pre-populated `{"jobs":0,"tasks":0}`
- `ping func(context.Context) error` — called in `Collect()` to determine `itential_up`

**Non-obvious behavior:** `task_error` and `task_cancel` are emitted as 0 for every server seen in `taskStarts` or `taskCompletes`, even before any error/cancel occurs. This prevents missing series that would break `rate()` in Grafana.

`Collect()` holds the mutex only for the snapshot copy, never during I/O.

### `internal/watcher`

Change stream consumer. Two goroutines run concurrently (one per collection) via `Start()`.

**`watchWithRetry`** is the reconnect loop. On each iteration:
- Calls the collection-specific watch function
- If it returns an error (network failure, election, etc.): logs, calls `health.RecordWatcherReconnect(name)`, backs off exponentially
- If it returns cleanly (normal close): resets backoff and reopens immediately
- Resume token is passed to the next iteration so MongoDB replays missed events

**`handleJobEvent` / `handleTaskEvent`** decode raw BSON and dispatch:
- `insert` → `RecordJobStart/TaskStart` + `ApplyStatusDelta(newStatus, +1)`
- `update` → delta old status by -1, new status by +1; call `RecordJobComplete/Error/Cancel` if terminal
- `replace` → same delta logic as update

**`jobStatusMap` / `taskStatusMap`** track the current status of every active (non-terminal) document. This allows `watchWithRetry` to compute the `-1` delta for the previous status on each transition. Keys are hex ObjectID strings. Terminal documents are deleted from the map to bound memory.

On initial open (not reconnect), `loadActiveJobs/loadActiveTasks` queries all non-terminal documents to prime these maps.

**`health` field** may be `nil` (used in tests). `watchWithRetry` guards with `if w.health != nil`.

### `internal/queries`

Self-contained MongoDB aggregation pipelines on a `Runner`. Every query:
- Specifies an explicit index hint — will return an error rather than fall back to a collection scan if the index is missing
- Sets `SetMaxTime` from the context deadline so MongoDB kills the server-side cursor if the client context expires
- Inherits `SecondaryPreferred` from the client

Queries used by the exporter:
- `JobsByStatus` — groups all jobs by status; index: `itential_status {status:1, _id:1}`
- `TasksByStatus` — groups all tasks by status; index: `itential_job_metrics_exporter_task_status_server {status:1, metrics.server_id:1}`

Other queries exist in the file (`TasksByServerID`, `TasksByActiveStatusAndServer`, `TasksByCompletedAndServer`) but are not currently called from `main.go`.

---

## Metrics Catalog

All metrics use the `itential_` prefix.

### Change Stream Counters (require `change_stream.enabled: true`)

| Metric | Type | Labels | Description |
|---|---|---|---|
| `itential_job_start` | Counter | — | Jobs inserted |
| `itential_job_complete` | Counter | — | Jobs → status `complete` |
| `itential_job_error` | Counter | — | Jobs → status `error` |
| `itential_job_cancel` | Counter | — | Jobs → status `canceled` |
| `itential_task_start` | Counter | `server_id` | Tasks inserted, by worker |
| `itential_task_complete` | Counter | `server_id` | Tasks → `complete`, by worker |
| `itential_task_error` | Counter | `server_id` | Tasks → `error`, by worker |
| `itential_task_cancel` | Counter | `server_id` | Tasks → `canceled`, by worker |

`server_id` is sourced from `metrics.server_id` on the task document.

### Status Gauges (change stream or polling)

| Metric | Type | Labels | Description |
|---|---|---|---|
| `itential_job_status_total` | Gauge | `status` | Current jobs per status |
| `itential_task_status_total` | Gauge | `status` | Current tasks per status |

Status values are dynamic — no config needed when new statuses appear.

### Exporter Health (always emitted from first scrape)

| Metric | Type | Labels | Description |
|---|---|---|---|
| `itential_build_info` | Gauge | `version` | Always 1. Version string injected at build time via `-ldflags` |
| `itential_up` | Gauge | — | 1 = MongoDB reachable, 0 = ping failed |
| `itential_scrape_duration_seconds` | Gauge | — | Full scrape duration including ping |
| `itential_watcher_reconnects_total` | Counter | `collection` | Change stream reconnects per collection |
| `itential_poll_errors_total` | Counter | `query` | Poll query failures per query type |

Health metrics are pre-initialized to 0 for `collection=jobs`, `collection=tasks`, `query=jobs`, `query=tasks` so they appear in output before any event occurs.

---

## HTTP Endpoints

| Path | Description |
|---|---|
| `/metrics` | Prometheus metrics (text or OpenMetrics) |
| `/healthz` | Returns `200 ok` — liveness probe, no MongoDB check |
| `/` | HTML landing page with version string and metrics link |

`/healthz` does **not** ping MongoDB. Use `itential_up == 0` for MongoDB connectivity alerts.

---

## CLI Flags

| Flag | Description |
|---|---|
| `--config` | Path to YAML config file |
| `--database` | Override MongoDB database name |
| `--listen-address` | Override listen address (e.g. `:9477`) |
| `--version` | Print version (injected at build via `-ldflags`) and exit 0 |

---

## Development Workflow

### Make Targets

```bash
make build                   # Native binary → dist/ (auto-detects OS/arch)
make test                    # All tests with race detector
make lint                    # golangci-lint
make hooks                   # Install git hooks from .githooks/ into .git/hooks/
make release-linux-amd64     # Cross-compile linux/amd64
make release-linux-arm64     # Cross-compile linux/arm64
make release-all             # linux/amd64 + linux/arm64 (used by CI release pipeline)
make release-darwin-arm64    # macOS Apple Silicon (local testing only, not distributed)
make release-darwin-amd64    # macOS Intel (local testing only, not distributed)
make install                 # Install linux/amd64 binary + config + systemd unit (needs root)
make clean                   # Remove dist/
```

### Git Hooks

Install with `make hooks`. Two hooks are enforced:

- **`pre-commit`** — validates the branch name format: `<type>/<description>` where type is one of `feature`, `fix`, `refactor`, `docs`, `chore`, and description uses only lowercase letters, numbers, and hyphens.
- **`commit-msg`** — enforces [Conventional Commits](https://www.conventionalcommits.org/) format: `type[(scope)]: description` (72 char max). Valid types: `feat`, `fix`, `docs`, `style`, `refactor`, `test`, `chore`, `perf`.

### CI Pipeline

`.github/workflows/release.yml` triggers on `v*` tags. It calls `make release-all` and uploads the artifacts to a GitHub Release with auto-generated notes. It also builds a multi-platform (`linux/amd64`, `linux/arm64`) container image and pushes it to `ghcr.io/itential/job-metrics-exporter` tagged with the version and `latest`.

`.github/workflows/pr-compliance.yml` runs PR title/description checks.

---

## Testing

No external services required. All tests are pure Go unit tests.

```bash
go test -v -race ./internal/...           # all packages
go test -v -race ./internal/collector/... # collector only
go test -v -race ./internal/watcher/...   # watcher only
go test -v -race ./internal/config/...    # config only
```

### Test Coverage by Package

**`internal/collector`**
Uses `prometheus/testutil.CollectAndCompare` with exact expected metric text. Tests cover:
- Job and task counter correctness at zero and after increments
- Task error/cancel zero-defaulting for known servers
- Status gauge replacement via `SetJobStatusCounts`
- Health metrics: `itential_up` success and failure paths, watcher reconnect and poll error counters at zero and after increments, scrape duration presence, concurrent write safety for all health methods

The `newTestCollector()` helper passes a no-op ping (`func(context.Context) error { return nil }`).

**`internal/watcher`**
Uses a `mockRecorder` (in `mock_recorder_test.go`) that implements `EventRecorder`. Tests send raw BSON payloads directly to `handleJobEvent` / `handleTaskEvent` to verify correct recorder calls for every operation type (insert, update, replace) and every terminal status. Health is passed as `nil`.

**`internal/config`**
Tests YAML loading, env var overlay precedence, and all validation error paths.

---

## Non-Obvious Behaviors and Invariants

- **`task_error` and `task_cancel` always emit 0 for known servers.** Any server seen in `taskStarts` or `taskCompletes` gets a 0-value entry for error and cancel before the first event occurs. This prevents missing series that break `rate()` in Grafana.

- **Status maps are memory-bounded.** The watcher deletes terminal documents (`complete`, `error`, `canceled`) from `jobStatusMap` / `taskStatusMap`. Without this, every completed job would accumulate in memory forever.

- **Resume tokens prevent data loss on reconnect.** The watcher passes the last resume token back into MongoDB on reconnect. MongoDB replays all events that occurred during the outage. The in-memory status maps do **not** need to be reloaded from MongoDB on reconnect.

- **The bootstrap poll only runs once.** It seeds the initial status gauge values at startup. After that, the change stream maintains the gauges via ±1 deltas. The bootstrap uses `SlowQueryTimeout` (default 4m) because it must scan the full jobs and tasks collections.

- **`polling.query_timeout` must be strictly less than `polling.interval`.** This is a startup validation rule. The gap is intentional: it allows the query to time out cleanly before the next poll tick fires.

- **`itential_up` adds latency to every Prometheus scrape.** The ping has a 3-second hard timeout inside `Collect()`. In normal operation this is fast, but if MongoDB is slow or unreachable, each scrape takes up to 3 seconds longer.

- **The MongoDB client read preference is always `SecondaryPreferred`.** If the replica set has no secondaries, MongoDB automatically falls back to the primary. This is invisible to the exporter.

- **Darwin binaries are not distributed.** `make release-all` only builds linux targets. Individual `release-darwin-*` targets exist for local development and are invoked manually. The CI pipeline does not produce darwin artifacts.

- **The version string is injected at build time.** `main.version` defaults to `"dev"`. The Makefile sets it via `-ldflags "-X main.version=$(VERSION)"` where `VERSION` comes from `git describe --tags --always --dirty`. A `-dirty` suffix means there were uncommitted changes at build time.

---

## Documentation Sync

When adding or modifying metrics, update both the Metrics Catalog in `CLAUDE.md` and the corresponding table in `README.md` — they are kept in sync manually.
