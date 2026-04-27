// Package watcher consumes MongoDB change streams on the jobs and tasks
// collections and records lifecycle events via an EventRecorder.
//
// Status gauges (itential_job_status_total, itential_task_status_total) are
// maintained incrementally: a one-time bootstrap query in main seeds the
// initial counts, and every subsequent change stream event applies a ±1 delta
// via ApplyJobStatusDelta / ApplyTaskStatusDelta. No periodic full-collection
// scans are needed after startup.
//
// Events handled:
//
//	insert              → RecordJobStart/RecordTaskStart + ApplyStatusDelta(newStatus, +1)
//	update (status)     → ApplyStatusDelta(old, -1) + ApplyStatusDelta(new, +1) + Record* if terminal
//	replace             → same delta logic as update
//
// The watcher tracks the current status of every active (non-terminal)
// document in an in-memory map so it can compute the old-status delta when a
// status transition arrives. This map is populated by an initial load query
// on first open (controlled by cfg.InitialLoad). On reconnect the resume
// token replays missed events, so the map stays correct without reloading.
//
// The change stream pipelines filter at the MongoDB level so only relevant
// events are delivered. The watcher reconnects automatically on failure using
// a resume token and backs off exponentially (1 s → 60 s).
//
// MongoDB requirements:
//   - Replica set (change streams require oplog access).
//   - read role on the database (grants changeStream on MongoDB 4.0+).
package watcher

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/itential/job-metrics-exporter/internal/collector"
	"github.com/itential/job-metrics-exporter/internal/config"
)

// terminalStatuses are the job/task statuses that indicate a document has
// finished processing and will not transition again.
var terminalStatuses = bson.A{"complete", "error", "canceled"}

func isTerminal(status string) bool {
	return status == "complete" || status == "error" || status == "canceled"
}

// Watcher watches the jobs and tasks collections for lifecycle events.
type Watcher struct {
	db       *mongo.Database
	recorder collector.EventRecorder
	logger   *slog.Logger
	cfg      config.ChangeStreamConfig

	// jobStatusMap and taskStatusMap track the current status of every active
	// (non-terminal) document. They allow the watcher to compute the correct
	// ±1 delta when a status-change event arrives without querying MongoDB.
	// Each map is accessed only within its own collection's goroutine — no
	// mutex is required.
	// Keys are string representations of the document _id (hex for ObjectID,
	// raw string for UUID-style _id values).
	jobStatusMap  map[string]string
	taskStatusMap map[string]string
}

// New creates a Watcher. Call Start to begin watching.
func New(db *mongo.Database, recorder collector.EventRecorder, cfg config.ChangeStreamConfig, logger *slog.Logger) *Watcher {
	return &Watcher{
		db:            db,
		recorder:      recorder,
		logger:        logger.With("component", "watcher"),
		cfg:           cfg,
		jobStatusMap:  make(map[string]string),
		taskStatusMap: make(map[string]string),
	}
}

// idKey converts a BSON _id value to a stable string key for the status maps.
// Tasks may use either a 12-byte ObjectID or a UUID string as their _id.
func idKey(v bson.RawValue) string {
	if oid, ok := v.ObjectIDOK(); ok {
		return oid.Hex()
	}
	if s, ok := v.StringValueOK(); ok {
		return s
	}
	return v.String()
}

// Start watches both collections and blocks until ctx is cancelled.
// Intended to be called in a goroutine from main.
func (w *Watcher) Start(ctx context.Context) {
	done := make(chan struct{}, 2)
	go func() {
		w.watchWithRetry(ctx, "jobs", w.watchJobs)
		done <- struct{}{}
	}()
	go func() {
		w.watchWithRetry(ctx, "tasks", w.watchTasks)
		done <- struct{}{}
	}()
	<-done
	<-done
	w.logger.Info("watcher stopped")
}

// watchWithRetry calls fn in a loop until ctx is cancelled, resuming from the
// last resume token. It backs off exponentially between reconnect attempts.
func (w *Watcher) watchWithRetry(ctx context.Context, name string, fn func(context.Context, bson.Raw) (bson.Raw, error)) {
	backoff := time.Second
	var resumeToken bson.Raw
	for {
		if ctx.Err() != nil {
			return
		}
		token, err := fn(ctx, resumeToken)
		if len(token) > 0 {
			resumeToken = token
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			w.logger.Warn("change stream error, will retry", "collection", name, "err", err, "backoff", backoff)
			backoff = min(backoff*2, 60*time.Second)
		} else {
			w.logger.Info("change stream closed cleanly, reopening", "collection", name)
			backoff = time.Second
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
	}
}

// ── Jobs ──────────────────────────────────────────────────────────────────────

// jobsPipeline delivers insert events and any update/replace where the status
// field changed. Capturing all status transitions (not only terminal ones)
// lets the watcher maintain accurate gauge counts via ±1 deltas.
var jobsPipeline = mongo.Pipeline{
	{{Key: "$match", Value: bson.D{
		{Key: "$or", Value: bson.A{
			bson.D{{Key: "operationType", Value: "insert"}},
			bson.D{
				{Key: "operationType", Value: "update"},
				{Key: "updateDescription.updatedFields.status", Value: bson.D{
					{Key: "$exists", Value: true},
				}},
			},
			bson.D{{Key: "operationType", Value: "replace"}},
		}},
	}}},
}

// loadActiveJobs queries all non-terminal jobs and records their current status
// in jobStatusMap. This primes the map so that the first status-change event
// for each document can compute the correct delta. Called only on initial open.
func (w *Watcher) loadActiveJobs(ctx context.Context) {
	lctx, cancel := context.WithTimeout(ctx, w.cfg.InitialLoadTimeout)
	defer cancel()

	cur, err := w.db.Collection("jobs").Find(lctx,
		bson.D{{Key: "status", Value: bson.D{{Key: "$nin", Value: terminalStatuses}}}},
		options.Find().SetProjection(bson.D{
			{Key: "_id", Value: 1},
			{Key: "status", Value: 1},
		}),
	)
	if err != nil {
		w.logger.Warn("initial job status load failed — gauge deltas may drift until restart", "err", err)
		return
	}
	defer cur.Close(lctx)

	count := 0
	for cur.Next(lctx) {
		var doc struct {
			ID     bson.RawValue `bson:"_id"`
			Status string        `bson:"status"`
		}
		if err := cur.Decode(&doc); err != nil {
			continue
		}
		w.jobStatusMap[idKey(doc.ID)] = doc.Status
		count++
	}
	if err := cur.Err(); err != nil {
		w.logger.Warn("initial job load cursor error", "err", err)
		return
	}
	w.logger.Info("initial job status load complete", "active_jobs", count)
}

func (w *Watcher) watchJobs(ctx context.Context, resumeToken bson.Raw) (bson.Raw, error) {
	// Initial open only: prime the status map so deltas are accurate from the start.
	// On reconnect the resume token replays missed events — no reload needed.
	if len(resumeToken) == 0 && w.cfg.InitialLoad {
		w.loadActiveJobs(ctx)
	}

	opts := options.ChangeStream().SetMaxAwaitTime(10 * time.Second)
	if len(resumeToken) > 0 {
		opts.SetResumeAfter(resumeToken)
	}

	stream, err := w.db.Collection("jobs").Watch(ctx, jobsPipeline, opts)
	if err != nil {
		return nil, fmt.Errorf("opening jobs change stream: %w", err)
	}
	defer stream.Close(ctx)

	w.logger.Info("watching jobs change stream")
	var lastToken bson.Raw
	for stream.Next(ctx) {
		rt := stream.ResumeToken()
		lastToken = make(bson.Raw, len(rt))
		copy(lastToken, rt)
		w.handleJobEvent(stream.Current)
	}
	if err := stream.Err(); err != nil && ctx.Err() == nil {
		return lastToken, fmt.Errorf("jobs change stream: %w", err)
	}
	return lastToken, nil
}

func (w *Watcher) handleJobEvent(raw bson.Raw) {
	var ev struct {
		OperationType string `bson:"operationType"`
		DocumentKey   struct {
			ID bson.RawValue `bson:"_id"`
		} `bson:"documentKey"`
		FullDocument struct {
			Status string `bson:"status"`
		} `bson:"fullDocument"`
		UpdateDescription struct {
			UpdatedFields struct {
				Status string `bson:"status"`
			} `bson:"updatedFields"`
		} `bson:"updateDescription"`
	}
	if err := bson.Unmarshal(raw, &ev); err != nil {
		w.logger.Warn("failed to decode job change event", "err", err)
		return
	}

	id := idKey(ev.DocumentKey.ID)

	switch ev.OperationType {
	case "insert":
		newStatus := ev.FullDocument.Status
		w.recorder.RecordJobStart()
		if newStatus != "" {
			w.jobStatusMap[id] = newStatus
			w.recorder.ApplyJobStatusDelta(newStatus, 1)
		}
		w.logger.Debug("job started", "status", newStatus)

	case "update":
		newStatus := ev.UpdateDescription.UpdatedFields.Status
		if newStatus == "" {
			return
		}
		if oldStatus := w.jobStatusMap[id]; oldStatus != "" {
			w.recorder.ApplyJobStatusDelta(oldStatus, -1)
		}
		w.recorder.ApplyJobStatusDelta(newStatus, 1)
		if isTerminal(newStatus) {
			delete(w.jobStatusMap, id)
		} else {
			w.jobStatusMap[id] = newStatus
		}
		switch newStatus {
		case "complete":
			w.recorder.RecordJobComplete()
			w.logger.Debug("job completed")
		case "error":
			w.recorder.RecordJobError()
			w.logger.Debug("job errored")
		case "canceled":
			w.recorder.RecordJobCancel()
			w.logger.Debug("job canceled")
		}

	case "replace":
		newStatus := ev.FullDocument.Status
		if newStatus == "" {
			return
		}
		oldStatus := w.jobStatusMap[id]
		if oldStatus != "" && oldStatus != newStatus {
			w.recorder.ApplyJobStatusDelta(oldStatus, -1)
			w.recorder.ApplyJobStatusDelta(newStatus, 1)
		}
		if isTerminal(newStatus) {
			delete(w.jobStatusMap, id)
			if !isTerminal(oldStatus) {
				switch newStatus {
				case "complete":
					w.recorder.RecordJobComplete()
				case "error":
					w.recorder.RecordJobError()
				case "canceled":
					w.recorder.RecordJobCancel()
				}
			}
		} else {
			w.jobStatusMap[id] = newStatus
		}
		w.logger.Debug("job replaced", "new_status", newStatus)
	}
}

// ── Tasks ─────────────────────────────────────────────────────────────────────

// tasksPipeline delivers insert events and any update/replace where the status
// field changed. Capturing all status transitions (not only terminal ones)
// lets the watcher maintain accurate gauge counts via ±1 deltas.
var tasksPipeline = mongo.Pipeline{
	{{Key: "$match", Value: bson.D{
		{Key: "$or", Value: bson.A{
			bson.D{{Key: "operationType", Value: "insert"}},
			bson.D{
				{Key: "operationType", Value: "update"},
				{Key: "updateDescription.updatedFields.status", Value: bson.D{
					{Key: "$exists", Value: true},
				}},
			},
			bson.D{{Key: "operationType", Value: "replace"}},
		}},
	}}},
}

// loadActiveTasks queries all non-terminal tasks and records their current
// status in taskStatusMap. Called only on initial open.
func (w *Watcher) loadActiveTasks(ctx context.Context) {
	lctx, cancel := context.WithTimeout(ctx, w.cfg.InitialLoadTimeout)
	defer cancel()

	cur, err := w.db.Collection("tasks").Find(lctx,
		bson.D{{Key: "status", Value: bson.D{{Key: "$nin", Value: terminalStatuses}}}},
		options.Find().SetProjection(bson.D{
			{Key: "_id", Value: 1},
			{Key: "status", Value: 1},
		}),
	)
	if err != nil {
		w.logger.Warn("initial task status load failed — gauge deltas may drift until restart", "err", err)
		return
	}
	defer cur.Close(lctx)

	count := 0
	for cur.Next(lctx) {
		var doc struct {
			ID     bson.RawValue `bson:"_id"`
			Status string        `bson:"status"`
		}
		if err := cur.Decode(&doc); err != nil {
			continue
		}
		w.taskStatusMap[idKey(doc.ID)] = doc.Status
		count++
	}
	if err := cur.Err(); err != nil {
		w.logger.Warn("initial task load cursor error", "err", err)
		return
	}
	w.logger.Info("initial task status load complete", "active_tasks", count)
}

func (w *Watcher) watchTasks(ctx context.Context, resumeToken bson.Raw) (bson.Raw, error) {
	// Initial open only: prime the status map so deltas are accurate from the start.
	// On reconnect the resume token replays missed events — no reload needed.
	if len(resumeToken) == 0 && w.cfg.InitialLoad {
		w.loadActiveTasks(ctx)
	}

	opts := options.ChangeStream().SetMaxAwaitTime(10 * time.Second).SetFullDocument(options.UpdateLookup)
	if len(resumeToken) > 0 {
		opts.SetResumeAfter(resumeToken)
	}

	stream, err := w.db.Collection("tasks").Watch(ctx, tasksPipeline, opts)
	if err != nil {
		return nil, fmt.Errorf("opening tasks change stream: %w", err)
	}
	defer stream.Close(ctx)

	w.logger.Info("watching tasks change stream")
	var lastToken bson.Raw
	for stream.Next(ctx) {
		rt := stream.ResumeToken()
		lastToken = make(bson.Raw, len(rt))
		copy(lastToken, rt)
		w.handleTaskEvent(stream.Current)
	}
	if err := stream.Err(); err != nil && ctx.Err() == nil {
		return lastToken, fmt.Errorf("tasks change stream: %w", err)
	}
	return lastToken, nil
}

func (w *Watcher) handleTaskEvent(raw bson.Raw) {
	var ev struct {
		OperationType string `bson:"operationType"`
		DocumentKey   struct {
			ID bson.RawValue `bson:"_id"`
		} `bson:"documentKey"`
		FullDocument struct {
			Status  string `bson:"status"`
			Metrics struct {
				ServerID string `bson:"server_id"`
			} `bson:"metrics"`
		} `bson:"fullDocument"`
		UpdateDescription struct {
			UpdatedFields struct {
				Status string `bson:"status"`
			} `bson:"updatedFields"`
		} `bson:"updateDescription"`
	}
	if err := bson.Unmarshal(raw, &ev); err != nil {
		w.logger.Warn("failed to decode task change event", "err", err)
		return
	}

	id := idKey(ev.DocumentKey.ID)
	serverID := ev.FullDocument.Metrics.ServerID

	switch ev.OperationType {
	case "insert":
		newStatus := ev.FullDocument.Status
		w.recorder.RecordTaskStart(serverID)
		if newStatus != "" {
			w.taskStatusMap[id] = newStatus
			w.recorder.ApplyTaskStatusDelta(newStatus, 1)
		}
		w.logger.Debug("task started", "server_id", serverID, "status", newStatus)

	case "update":
		newStatus := ev.UpdateDescription.UpdatedFields.Status
		if newStatus == "" {
			return
		}
		if oldStatus := w.taskStatusMap[id]; oldStatus != "" {
			w.recorder.ApplyTaskStatusDelta(oldStatus, -1)
		}
		w.recorder.ApplyTaskStatusDelta(newStatus, 1)
		if isTerminal(newStatus) {
			delete(w.taskStatusMap, id)
		} else {
			w.taskStatusMap[id] = newStatus
		}
		switch newStatus {
		case "complete":
			w.recorder.RecordTaskComplete(serverID)
			w.logger.Debug("task completed", "server_id", serverID)
		case "error":
			w.recorder.RecordTaskError(serverID)
			w.logger.Debug("task errored", "server_id", serverID)
		case "canceled":
			w.recorder.RecordTaskCancel(serverID)
			w.logger.Debug("task canceled", "server_id", serverID)
		}

	case "replace":
		newStatus := ev.FullDocument.Status
		if newStatus == "" {
			return
		}
		oldStatus := w.taskStatusMap[id]
		if oldStatus != "" && oldStatus != newStatus {
			w.recorder.ApplyTaskStatusDelta(oldStatus, -1)
			w.recorder.ApplyTaskStatusDelta(newStatus, 1)
		}
		if isTerminal(newStatus) {
			delete(w.taskStatusMap, id)
			if !isTerminal(oldStatus) {
				switch newStatus {
				case "complete":
					w.recorder.RecordTaskComplete(serverID)
				case "error":
					w.recorder.RecordTaskError(serverID)
				case "canceled":
					w.recorder.RecordTaskCancel(serverID)
				}
			}
		} else {
			w.taskStatusMap[id] = newStatus
		}
		w.logger.Debug("task replaced", "server_id", serverID, "new_status", newStatus)
	}
}
