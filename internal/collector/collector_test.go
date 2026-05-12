package collector

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func newTestCollector() *Collector {
	return New("test", func(context.Context) error { return nil }, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestJobCounters(t *testing.T) {
	c := newTestCollector()
	c.RecordJobStart()
	c.RecordJobStart()
	c.RecordJobComplete()
	c.RecordJobError()
	c.RecordJobCancel()

	expected := `
# HELP itential_job_cancel Number of jobs that reached status 'canceled' in the current collection window.
# TYPE itential_job_cancel counter
itential_job_cancel 1
# HELP itential_job_complete Number of jobs that reached status 'complete' in the current collection window.
# TYPE itential_job_complete counter
itential_job_complete 1
# HELP itential_job_error Number of jobs that reached status 'error' in the current collection window.
# TYPE itential_job_error counter
itential_job_error 1
# HELP itential_job_start Number of jobs started (inserted) in the current collection window.
# TYPE itential_job_start counter
itential_job_start 2
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expected),
		"itential_job_start", "itential_job_complete", "itential_job_error", "itential_job_cancel",
	); err != nil {
		t.Error(err)
	}
}

func TestJobCounters_InitialZero(t *testing.T) {
	c := newTestCollector()

	expected := `
# HELP itential_job_cancel Number of jobs that reached status 'canceled' in the current collection window.
# TYPE itential_job_cancel counter
itential_job_cancel 0
# HELP itential_job_complete Number of jobs that reached status 'complete' in the current collection window.
# TYPE itential_job_complete counter
itential_job_complete 0
# HELP itential_job_error Number of jobs that reached status 'error' in the current collection window.
# TYPE itential_job_error counter
itential_job_error 0
# HELP itential_job_start Number of jobs started (inserted) in the current collection window.
# TYPE itential_job_start counter
itential_job_start 0
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expected),
		"itential_job_start", "itential_job_complete", "itential_job_error", "itential_job_cancel",
	); err != nil {
		t.Error(err)
	}
}

func TestTaskCounters(t *testing.T) {
	c := newTestCollector()
	c.RecordTaskStart("server-1")
	c.RecordTaskStart("server-1")
	c.RecordTaskStart("server-2")
	c.RecordTaskComplete("server-1")
	c.RecordTaskError("server-2")
	c.RecordTaskCancel("server-1")

	expected := `
# HELP itential_task_cancel Number of tasks that reached status 'canceled' in the current collection window.
# TYPE itential_task_cancel counter
itential_task_cancel{server_id="server-1"} 1
itential_task_cancel{server_id="server-2"} 0
# HELP itential_task_complete Number of tasks that reached status 'complete' in the current collection window.
# TYPE itential_task_complete counter
itential_task_complete{server_id="server-1"} 1
# HELP itential_task_error Number of tasks that reached status 'error' in the current collection window.
# TYPE itential_task_error counter
itential_task_error{server_id="server-1"} 0
itential_task_error{server_id="server-2"} 1
# HELP itential_task_start Number of tasks started (inserted) in the current collection window.
# TYPE itential_task_start counter
itential_task_start{server_id="server-1"} 2
itential_task_start{server_id="server-2"} 1
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expected),
		"itential_task_start", "itential_task_complete", "itential_task_error", "itential_task_cancel",
	); err != nil {
		t.Error(err)
	}
}

func TestTaskErrorAndCancelDefaultToZeroForSeenServers(t *testing.T) {
	c := newTestCollector()
	c.RecordTaskStart("server-1")
	c.RecordTaskComplete("server-2")
	// Neither server has errors or cancels — they should default to 0.

	expected := `
# HELP itential_task_cancel Number of tasks that reached status 'canceled' in the current collection window.
# TYPE itential_task_cancel counter
itential_task_cancel{server_id="server-1"} 0
itential_task_cancel{server_id="server-2"} 0
# HELP itential_task_error Number of tasks that reached status 'error' in the current collection window.
# TYPE itential_task_error counter
itential_task_error{server_id="server-1"} 0
itential_task_error{server_id="server-2"} 0
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expected),
		"itential_task_error", "itential_task_cancel",
	); err != nil {
		t.Error(err)
	}
}

func TestSetJobStatusCounts(t *testing.T) {
	c := newTestCollector()
	c.SetJobStatusCounts(map[string]int64{
		"running":  5,
		"complete": 100,
		"error":    2,
	})

	expected := `
# HELP itential_job_status_total Current number of jobs per status (snapshot from background query).
# TYPE itential_job_status_total gauge
itential_job_status_total{status="complete"} 100
itential_job_status_total{status="error"} 2
itential_job_status_total{status="running"} 5
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expected), "itential_job_status_total"); err != nil {
		t.Error(err)
	}
}

func TestSetTaskStatusCounts(t *testing.T) {
	c := newTestCollector()
	c.SetTaskStatusCounts(map[string]int64{
		"running": 10,
		"error":   3,
	})

	expected := `
# HELP itential_task_status_total Current number of tasks per status (snapshot from background query).
# TYPE itential_task_status_total gauge
itential_task_status_total{status="error"} 3
itential_task_status_total{status="running"} 10
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expected), "itential_task_status_total"); err != nil {
		t.Error(err)
	}
}

func TestSetJobStatusCounts_Replaces(t *testing.T) {
	c := newTestCollector()
	c.SetJobStatusCounts(map[string]int64{"running": 5})
	c.SetJobStatusCounts(map[string]int64{"running": 10, "complete": 3})

	expected := `
# HELP itential_job_status_total Current number of jobs per status (snapshot from background query).
# TYPE itential_job_status_total gauge
itential_job_status_total{status="complete"} 3
itential_job_status_total{status="running"} 10
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expected), "itential_job_status_total"); err != nil {
		t.Error(err)
	}
}

func TestConcurrentRecordJob(t *testing.T) {
	c := newTestCollector()
	const goroutines = 50
	const recordsEach = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for range recordsEach {
				c.RecordJobStart()
				c.RecordJobComplete()
				c.RecordJobError()
				c.RecordJobCancel()
			}
		}()
	}
	wg.Wait()

	expected := `
# HELP itential_job_cancel Number of jobs that reached status 'canceled' in the current collection window.
# TYPE itential_job_cancel counter
itential_job_cancel 5000
# HELP itential_job_complete Number of jobs that reached status 'complete' in the current collection window.
# TYPE itential_job_complete counter
itential_job_complete 5000
# HELP itential_job_error Number of jobs that reached status 'error' in the current collection window.
# TYPE itential_job_error counter
itential_job_error 5000
# HELP itential_job_start Number of jobs started (inserted) in the current collection window.
# TYPE itential_job_start counter
itential_job_start 5000
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expected),
		"itential_job_start", "itential_job_complete", "itential_job_error", "itential_job_cancel",
	); err != nil {
		t.Error(err)
	}
}

func TestConcurrentRecordTask(t *testing.T) {
	c := newTestCollector()
	const goroutines = 50
	const recordsEach = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for range recordsEach {
				c.RecordTaskStart("server-1")
				c.RecordTaskComplete("server-1")
				c.RecordTaskError("server-1")
				c.RecordTaskCancel("server-1")
			}
		}()
	}
	wg.Wait()

	expected := `
# HELP itential_task_cancel Number of tasks that reached status 'canceled' in the current collection window.
# TYPE itential_task_cancel counter
itential_task_cancel{server_id="server-1"} 5000
# HELP itential_task_complete Number of tasks that reached status 'complete' in the current collection window.
# TYPE itential_task_complete counter
itential_task_complete{server_id="server-1"} 5000
# HELP itential_task_error Number of tasks that reached status 'error' in the current collection window.
# TYPE itential_task_error counter
itential_task_error{server_id="server-1"} 5000
# HELP itential_task_start Number of tasks started (inserted) in the current collection window.
# TYPE itential_task_start counter
itential_task_start{server_id="server-1"} 5000
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expected),
		"itential_task_start", "itential_task_complete", "itential_task_error", "itential_task_cancel",
	); err != nil {
		t.Error(err)
	}
}

// ── Health metric tests ───────────────────────────────────────────────────────

func TestHealthUp_Success(t *testing.T) {
	c := newTestCollector() // no-op ping → always returns nil
	expected := `
# HELP itential_up 1 if the exporter can reach MongoDB, 0 otherwise.
# TYPE itential_up gauge
itential_up 1
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expected), "itential_up"); err != nil {
		t.Error(err)
	}
}

func TestHealthUp_Failure(t *testing.T) {
	c := New("test", func(context.Context) error { return errors.New("connection refused") },
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	expected := `
# HELP itential_up 1 if the exporter can reach MongoDB, 0 otherwise.
# TYPE itential_up gauge
itential_up 0
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expected), "itential_up"); err != nil {
		t.Error(err)
	}
}

func TestHealthWatcherReconnects_InitialZero(t *testing.T) {
	c := newTestCollector()
	expected := `
# HELP itential_watcher_reconnects_total Total number of change stream reconnects per collection.
# TYPE itential_watcher_reconnects_total counter
itential_watcher_reconnects_total{collection="jobs"} 0
itential_watcher_reconnects_total{collection="tasks"} 0
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expected), "itential_watcher_reconnects_total"); err != nil {
		t.Error(err)
	}
}

func TestHealthWatcherReconnects_Increment(t *testing.T) {
	c := newTestCollector()
	c.RecordWatcherReconnect("jobs")
	c.RecordWatcherReconnect("jobs")
	c.RecordWatcherReconnect("tasks")
	expected := `
# HELP itential_watcher_reconnects_total Total number of change stream reconnects per collection.
# TYPE itential_watcher_reconnects_total counter
itential_watcher_reconnects_total{collection="jobs"} 2
itential_watcher_reconnects_total{collection="tasks"} 1
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expected), "itential_watcher_reconnects_total"); err != nil {
		t.Error(err)
	}
}

func TestHealthPollErrors_InitialZero(t *testing.T) {
	c := newTestCollector()
	expected := `
# HELP itential_poll_errors_total Total number of background poll query errors per query type.
# TYPE itential_poll_errors_total counter
itential_poll_errors_total{query="jobs"} 0
itential_poll_errors_total{query="tasks"} 0
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expected), "itential_poll_errors_total"); err != nil {
		t.Error(err)
	}
}

func TestHealthPollErrors_Increment(t *testing.T) {
	c := newTestCollector()
	c.RecordPollError("jobs")
	c.RecordPollError("tasks")
	c.RecordPollError("tasks")
	expected := `
# HELP itential_poll_errors_total Total number of background poll query errors per query type.
# TYPE itential_poll_errors_total counter
itential_poll_errors_total{query="jobs"} 1
itential_poll_errors_total{query="tasks"} 2
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expected), "itential_poll_errors_total"); err != nil {
		t.Error(err)
	}
}

func TestHealthScrapeDuration_Present(t *testing.T) {
	c := newTestCollector()
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() == "itential_scrape_duration_seconds" {
			if len(mf.GetMetric()) != 1 {
				t.Fatalf("expected 1 sample, got %d", len(mf.GetMetric()))
			}
			if v := mf.GetMetric()[0].GetGauge().GetValue(); v < 0 {
				t.Errorf("scrape_duration_seconds is negative: %v", v)
			}
			return
		}
	}
	t.Fatal("itential_scrape_duration_seconds not found")
}

func TestConcurrentHealthRecording(t *testing.T) {
	c := newTestCollector()
	const goroutines = 50
	const recordsEach = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for range recordsEach {
				c.RecordWatcherReconnect("jobs")
				c.RecordWatcherReconnect("tasks")
				c.RecordPollError("jobs")
				c.RecordPollError("tasks")
			}
		}()
	}
	wg.Wait()

	expected := `
# HELP itential_poll_errors_total Total number of background poll query errors per query type.
# TYPE itential_poll_errors_total counter
itential_poll_errors_total{query="jobs"} 5000
itential_poll_errors_total{query="tasks"} 5000
# HELP itential_watcher_reconnects_total Total number of change stream reconnects per collection.
# TYPE itential_watcher_reconnects_total counter
itential_watcher_reconnects_total{collection="jobs"} 5000
itential_watcher_reconnects_total{collection="tasks"} 5000
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expected),
		"itential_watcher_reconnects_total", "itential_poll_errors_total",
	); err != nil {
		t.Error(err)
	}
}
