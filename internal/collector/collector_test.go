package collector

import (
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func newTestCollector() *Collector {
	return New(slog.New(slog.NewTextHandler(io.Discard, nil)))
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
