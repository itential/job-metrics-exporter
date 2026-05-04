// Package collector implements a Prometheus Collector that tracks job and task
// lifecycle events observed via the MongoDB change stream.
//
// Counters maintained:
//
//	itential_job_start     — jobs inserted (ever-increasing)
//	itential_job_complete  — jobs whose status transitioned to "complete"
//	itential_job_cancel    — jobs whose status transitioned to "canceled"
//	itential_task_start    — tasks inserted (ever-increasing)
//	itential_task_complete — tasks whose status transitioned to "complete"
//	itential_task_cancel   — tasks whose status transitioned to "canceled"
//
// Use rate() or increase() in Grafana to graph activity over time.
package collector

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const namespace = "itential"

// HealthRecorder is called by the watcher and poller to record exporter-internal
// health events. It is implemented by Collector.
type HealthRecorder interface {
	RecordWatcherReconnect(collection string)
}

// EventRecorder is called by the change stream watcher to record lifecycle
// events. It is implemented by Collector.
type EventRecorder interface {
	RecordJobStart()
	RecordJobComplete()
	RecordJobError()
	RecordJobCancel()
	RecordTaskStart(serverID string)
	RecordTaskComplete(serverID string)
	RecordTaskError(serverID string)
	RecordTaskCancel(serverID string)
	// ApplyJobStatusDelta adjusts the job status gauge by delta (+1 or -1).
	// Called by the change stream watcher on every status transition so the
	// gauge stays current without periodic full collection scans.
	ApplyJobStatusDelta(status string, delta int64)
	// ApplyTaskStatusDelta adjusts the task status gauge by delta (+1 or -1).
	ApplyTaskStatusDelta(status string, delta int64)
}

// Collector counts job and task lifecycle events and exposes them as Prometheus
// counters. All fields are protected by mu.
type Collector struct {
	mu            sync.Mutex
	jobStarts     int64
	jobCompletes  int64
	jobErrors     int64
	jobCancels    int64
	taskStarts    map[string]int64
	taskCompletes map[string]int64
	taskErrors    map[string]int64
	taskCancels   map[string]int64

	// jobStatusCounts and taskStatusCounts are populated by the background poller.
	// Keys are status strings (e.g. "running", "complete", "error").
	jobStatusCounts  map[string]int64
	taskStatusCounts map[string]int64

	// health counters — initialized to zero for known keys so they appear in
	// output from the first scrape even before any event occurs.
	watcherReconnects map[string]int64
	pollErrors        map[string]int64

	ping   func(context.Context) error
	logger *slog.Logger

	jobStartDesc         *prometheus.Desc
	jobCompleteDesc      *prometheus.Desc
	jobErrorDesc         *prometheus.Desc
	taskStartDesc        *prometheus.Desc
	taskCompleteDesc     *prometheus.Desc
	taskErrorDesc        *prometheus.Desc
	taskCancelDesc       *prometheus.Desc
	jobCancelDesc        *prometheus.Desc
	jobStatusDesc        *prometheus.Desc
	taskStatusDesc       *prometheus.Desc
	upDesc               *prometheus.Desc
	scrapeDurationDesc   *prometheus.Desc
	watcherReconnectDesc *prometheus.Desc
	pollErrorDesc        *prometheus.Desc
}

// New creates a Collector. ping is called on every scrape to determine
// itential_up; pass nil to disable the ping (up will always be 1).
func New(ping func(context.Context) error, logger *slog.Logger) *Collector {
	return &Collector{
		ping:              ping,
		logger:            logger,
		taskStarts:        make(map[string]int64),
		taskCompletes:     make(map[string]int64),
		taskErrors:        make(map[string]int64),
		taskCancels:       make(map[string]int64),
		jobStatusCounts:   make(map[string]int64),
		taskStatusCounts:  make(map[string]int64),
		watcherReconnects: map[string]int64{"jobs": 0, "tasks": 0},
		pollErrors:        map[string]int64{"jobs": 0, "tasks": 0},
		jobStartDesc: prometheus.NewDesc(
			namespace+"_job_start",
			"Number of jobs started (inserted) in the current collection window.",
			nil, nil,
		),
		jobCompleteDesc: prometheus.NewDesc(
			namespace+"_job_complete",
			"Number of jobs that reached status 'complete' in the current collection window.",
			nil, nil,
		),
		jobErrorDesc: prometheus.NewDesc(
			namespace+"_job_error",
			"Number of jobs that reached status 'error' in the current collection window.",
			nil, nil,
		),
		taskStartDesc: prometheus.NewDesc(
			namespace+"_task_start",
			"Number of tasks started (inserted) in the current collection window.",
			[]string{"server_id"}, nil,
		),
		taskCompleteDesc: prometheus.NewDesc(
			namespace+"_task_complete",
			"Number of tasks that reached status 'complete' in the current collection window.",
			[]string{"server_id"}, nil,
		),
		taskErrorDesc: prometheus.NewDesc(
			namespace+"_task_error",
			"Number of tasks that reached status 'error' in the current collection window.",
			[]string{"server_id"}, nil,
		),
		jobCancelDesc: prometheus.NewDesc(
			namespace+"_job_cancel",
			"Number of jobs that reached status 'canceled' in the current collection window.",
			nil, nil,
		),
		taskCancelDesc: prometheus.NewDesc(
			namespace+"_task_cancel",
			"Number of tasks that reached status 'canceled' in the current collection window.",
			[]string{"server_id"}, nil,
		),
		jobStatusDesc: prometheus.NewDesc(
			namespace+"_job_status_total",
			"Current number of jobs per status (snapshot from background query).",
			[]string{"status"}, nil,
		),
		taskStatusDesc: prometheus.NewDesc(
			namespace+"_task_status_total",
			"Current number of tasks per status (snapshot from background query).",
			[]string{"status"}, nil,
		),
		upDesc: prometheus.NewDesc(
			namespace+"_up",
			"1 if the exporter can reach MongoDB, 0 otherwise.",
			nil, nil,
		),
		scrapeDurationDesc: prometheus.NewDesc(
			namespace+"_scrape_duration_seconds",
			"Duration of the last metrics scrape in seconds.",
			nil, nil,
		),
		watcherReconnectDesc: prometheus.NewDesc(
			namespace+"_watcher_reconnects_total",
			"Total number of change stream reconnects per collection.",
			[]string{"collection"}, nil,
		),
		pollErrorDesc: prometheus.NewDesc(
			namespace+"_poll_errors_total",
			"Total number of background poll query errors per query type.",
			[]string{"query"}, nil,
		),
	}
}

// SetJobStatusCounts replaces the cached job status counts with a fresh snapshot.
// Used by the bootstrap poll on startup to seed the initial gauge values.
func (c *Collector) SetJobStatusCounts(counts map[string]int64) {
	c.mu.Lock()
	c.jobStatusCounts = counts
	c.mu.Unlock()
}

// SetTaskStatusCounts replaces the cached task status counts with a fresh snapshot.
// Used by the bootstrap poll on startup to seed the initial gauge values.
func (c *Collector) SetTaskStatusCounts(counts map[string]int64) {
	c.mu.Lock()
	c.taskStatusCounts = counts
	c.mu.Unlock()
}

// ApplyJobStatusDelta increments or decrements the gauge for a single job status.
// delta should be +1 (document entered this status) or -1 (document left it).
// Entries that reach zero are removed so the gauge emits no stale series.
func (c *Collector) ApplyJobStatusDelta(status string, delta int64) {
	c.mu.Lock()
	c.jobStatusCounts[status] += delta
	if c.jobStatusCounts[status] <= 0 {
		delete(c.jobStatusCounts, status)
	}
	c.mu.Unlock()
}

// ApplyTaskStatusDelta increments or decrements the gauge for a single task status.
// delta should be +1 (document entered this status) or -1 (document left it).
// Entries that reach zero are removed so the gauge emits no stale series.
func (c *Collector) ApplyTaskStatusDelta(status string, delta int64) {
	c.mu.Lock()
	c.taskStatusCounts[status] += delta
	if c.taskStatusCounts[status] <= 0 {
		delete(c.taskStatusCounts, status)
	}
	c.mu.Unlock()
}

// RecordJobStart increments the job start counter.
func (c *Collector) RecordJobStart() {
	c.mu.Lock()
	c.jobStarts++
	c.mu.Unlock()
}

// RecordJobComplete increments the job complete counter.
func (c *Collector) RecordJobComplete() {
	c.mu.Lock()
	c.jobCompletes++
	c.mu.Unlock()
}

// RecordJobError increments the job error counter.
func (c *Collector) RecordJobError() {
	c.mu.Lock()
	c.jobErrors++
	c.mu.Unlock()
}

// RecordJobCancel increments the job cancel counter.
func (c *Collector) RecordJobCancel() {
	c.mu.Lock()
	c.jobCancels++
	c.mu.Unlock()
}

// RecordTaskStart increments the task start counter for the given server.
func (c *Collector) RecordTaskStart(serverID string) {
	c.mu.Lock()
	c.taskStarts[serverID]++
	c.mu.Unlock()
}

// RecordTaskComplete increments the task complete counter for the given server.
func (c *Collector) RecordTaskComplete(serverID string) {
	c.mu.Lock()
	c.taskCompletes[serverID]++
	c.mu.Unlock()
}

// RecordTaskError increments the task error counter for the given server.
func (c *Collector) RecordTaskError(serverID string) {
	c.mu.Lock()
	c.taskErrors[serverID]++
	c.mu.Unlock()
}

// RecordTaskCancel increments the task cancel counter for the given server.
func (c *Collector) RecordTaskCancel(serverID string) {
	c.mu.Lock()
	c.taskCancels[serverID]++
	c.mu.Unlock()
}

// RecordWatcherReconnect increments the reconnect counter for the given collection.
func (c *Collector) RecordWatcherReconnect(collection string) {
	c.mu.Lock()
	c.watcherReconnects[collection]++
	c.mu.Unlock()
}

// RecordPollError increments the poll error counter for the given query type.
func (c *Collector) RecordPollError(query string) {
	c.mu.Lock()
	c.pollErrors[query]++
	c.mu.Unlock()
}

// Describe implements prometheus.Collector.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.jobStartDesc
	ch <- c.jobCompleteDesc
	ch <- c.jobErrorDesc
	ch <- c.jobCancelDesc
	ch <- c.taskStartDesc
	ch <- c.taskCompleteDesc
	ch <- c.taskErrorDesc
	ch <- c.taskCancelDesc
	ch <- c.jobStatusDesc
	ch <- c.taskStatusDesc
	ch <- c.upDesc
	ch <- c.scrapeDurationDesc
	ch <- c.watcherReconnectDesc
	ch <- c.pollErrorDesc
}

// Collect implements prometheus.Collector. It snapshots the current counters
// and cached query results without blocking the change stream watcher or poller.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	start := time.Now()

	up := float64(1)
	if c.ping != nil {
		pingCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if err := c.ping(pingCtx); err != nil {
			up = 0
			c.logger.Warn("MongoDB ping failed during scrape", "err", err)
		}
		cancel()
	}

	c.mu.Lock()
	js, jc, je, jcanc := c.jobStarts, c.jobCompletes, c.jobErrors, c.jobCancels
	ts := make(map[string]int64, len(c.taskStarts))
	for k, v := range c.taskStarts {
		ts[k] = v
	}
	tc := make(map[string]int64, len(c.taskCompletes))
	for k, v := range c.taskCompletes {
		tc[k] = v
	}
	te := make(map[string]int64, len(c.taskErrors))
	for k, v := range c.taskErrors {
		te[k] = v
	}
	tcanc := make(map[string]int64, len(c.taskCancels))
	for k, v := range c.taskCancels {
		tcanc[k] = v
	}
	jsc := make(map[string]int64, len(c.jobStatusCounts))
	for k, v := range c.jobStatusCounts {
		jsc[k] = v
	}
	tsc := make(map[string]int64, len(c.taskStatusCounts))
	for k, v := range c.taskStatusCounts {
		tsc[k] = v
	}
	wr := make(map[string]int64, len(c.watcherReconnects))
	for k, v := range c.watcherReconnects {
		wr[k] = v
	}
	pe := make(map[string]int64, len(c.pollErrors))
	for k, v := range c.pollErrors {
		pe[k] = v
	}
	c.mu.Unlock()

	duration := time.Since(start).Seconds()
	ch <- prometheus.MustNewConstMetric(c.upDesc, prometheus.GaugeValue, up)
	ch <- prometheus.MustNewConstMetric(c.scrapeDurationDesc, prometheus.GaugeValue, duration)
	for collection, count := range wr {
		ch <- prometheus.MustNewConstMetric(c.watcherReconnectDesc, prometheus.CounterValue, float64(count), collection)
	}
	for query, count := range pe {
		ch <- prometheus.MustNewConstMetric(c.pollErrorDesc, prometheus.CounterValue, float64(count), query)
	}

	ch <- prometheus.MustNewConstMetric(c.jobStartDesc, prometheus.CounterValue, float64(js))
	ch <- prometheus.MustNewConstMetric(c.jobCompleteDesc, prometheus.CounterValue, float64(jc))
	ch <- prometheus.MustNewConstMetric(c.jobErrorDesc, prometheus.CounterValue, float64(je))
	ch <- prometheus.MustNewConstMetric(c.jobCancelDesc, prometheus.CounterValue, float64(jcanc))
	for serverID, count := range ts {
		ch <- prometheus.MustNewConstMetric(c.taskStartDesc, prometheus.CounterValue, float64(count), serverID)
	}
	for serverID, count := range tc {
		ch <- prometheus.MustNewConstMetric(c.taskCompleteDesc, prometheus.CounterValue, float64(count), serverID)
	}
	// Emit task_error and task_cancel for every server seen in starts or
	// completes, defaulting to 0. Without this, the metric is absent until the
	// first event occurs, which breaks Grafana rate() queries and alerting rules.
	for serverID := range ts {
		if _, ok := te[serverID]; !ok {
			te[serverID] = 0
		}
		if _, ok := tcanc[serverID]; !ok {
			tcanc[serverID] = 0
		}
	}
	for serverID := range tc {
		if _, ok := te[serverID]; !ok {
			te[serverID] = 0
		}
		if _, ok := tcanc[serverID]; !ok {
			tcanc[serverID] = 0
		}
	}
	for serverID, count := range te {
		ch <- prometheus.MustNewConstMetric(c.taskErrorDesc, prometheus.CounterValue, float64(count), serverID)
	}
	for serverID, count := range tcanc {
		ch <- prometheus.MustNewConstMetric(c.taskCancelDesc, prometheus.CounterValue, float64(count), serverID)
	}
	for status, count := range jsc {
		ch <- prometheus.MustNewConstMetric(c.jobStatusDesc, prometheus.GaugeValue, float64(count), status)
	}
	for status, count := range tsc {
		ch <- prometheus.MustNewConstMetric(c.taskStatusDesc, prometheus.GaugeValue, float64(count), status)
	}
}
