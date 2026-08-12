// Package queries contains all MongoDB aggregation pipelines used by the
// exporter. Each function is self-contained, explicitly specifies its index
// hint, and is documented with the index it relies on.
//
// To add a new metric:
//  1. Add a new exported function on Runner returning a typed result.
//  2. Call it from collector.go in the parallel refresh block.
//  3. Register a new prometheus.Desc in collector.go and emit it in Collect.
package queries

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// StatusCount is a (status, count) pair returned by status grouping queries.
type StatusCount struct {
	Status string
	Count  int64
}

// StatusServerCount is a (status, server_id, count) triple returned by
// TasksByStatusAndServer.
type StatusServerCount struct {
	Status   string
	ServerID string
	Count    int64
}

// Runner executes metric queries against a MongoDB database.
// All queries inherit the SecondaryPreferred read preference set on the client
// by mongoclient.New — aggregations are never sent to the primary.
type Runner struct {
	db              *mongo.Database
	taskStatusIndex string
}

// NewRunner returns a Runner bound to db. taskStatusIndex is the name of the
// index hinted by all task status/server queries (must cover
// {status:1, "metrics.server_id":1}) — see config.QueriesConfig.TaskStatusIndex.
func NewRunner(db *mongo.Database, taskStatusIndex string) *Runner {
	return &Runner{db: db, taskStatusIndex: taskStatusIndex}
}

// ── Status groupings ──────────────────────────────────────────────────────────

// JobsByStatus groups jobs by status.
// Index hint: itential_status {status:1, _id:1}
// This is a fully covered IXSCAN — documents are never fetched.
// Expensive: scans the full jobs collection.
// Use on a slow refresh interval.
//
// If ctx carries a deadline, SetMaxTime is applied so MongoDB kills the
// server-side query when the deadline expires — not just the client cursor.
// Without this, a timed-out client leaves ghost queries running on the server.
func (r *Runner) JobsByStatus(ctx context.Context) ([]StatusCount, error) {
	pipeline := groupByField("$status")
	opts := options.Aggregate().
		SetHint("itential_status").
		SetAllowDiskUse(false)
	if deadline, ok := ctx.Deadline(); ok {
		opts.SetMaxTime(time.Until(deadline))
	}

	return runStatusAgg(ctx, r.db.Collection("jobs"), pipeline, opts)
}

// TasksByStatus groups all tasks by status.
// Index hint: r.taskStatusIndex (configurable; default iap_status_server_id) {status:1, metrics.server_id:1}
// The leading status field covers the group-by as an index scan.
// Expensive: scans the full tasks collection.
// Use on a slow refresh interval.
//
// If ctx carries a deadline, SetMaxTime is applied so MongoDB kills the
// server-side query when the deadline expires — not just the client cursor.
// Without this, a timed-out client leaves ghost queries running on the server.
func (r *Runner) TasksByStatus(ctx context.Context) ([]StatusCount, error) {
	pipeline := groupByField("$status")
	opts := options.Aggregate().
		SetHint(r.taskStatusIndex).
		SetAllowDiskUse(false)
	if deadline, ok := ctx.Deadline(); ok {
		opts.SetMaxTime(time.Until(deadline))
	}

	return runStatusAgg(ctx, r.db.Collection("tasks"), pipeline, opts)
}

// TasksByServerID counts running tasks grouped by metrics.server_id.
// Index hint: r.taskStatusIndex (configurable; default iap_status_server_id) {status:1, metrics.server_id:1}
// The $match restricts the scan to running tasks only, so the index range is
// bounded by the cardinality of running tasks rather than the full collection.
// The StatusCount.Status field carries the server_id value.
// Cheap enough for frequent refresh: bounded to running tasks only.
func (r *Runner) TasksByServerID(ctx context.Context) ([]StatusCount, error) {
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.D{{Key: "status", Value: "running"}}}},
		{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: "$metrics.server_id"},
			{Key: "count", Value: bson.D{{Key: "$sum", Value: 1}}},
		}}},
	}
	opts := options.Aggregate().
		SetHint(r.taskStatusIndex).
		SetAllowDiskUse(false)

	return runStatusAgg(ctx, r.db.Collection("tasks"), pipeline, opts)
}

// TasksByActiveStatusAndServer groups tasks with status "running" or "error"
// by (status, metrics.server_id). Intended for frequent collection.
// Index hint: r.taskStatusIndex (configurable; default iap_status_server_id) {status:1, metrics.server_id:1}
// The $match bounds the scan to active tasks only, so the index range is
// O(running+error tasks) rather than O(all tasks).
// Cheap enough for frequent refresh: bounded to active tasks only.
func (r *Runner) TasksByActiveStatusAndServer(ctx context.Context) ([]StatusServerCount, error) {
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.D{
			{Key: "status", Value: bson.D{{Key: "$in", Value: bson.A{"running", "error"}}}},
		}}},
		{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: bson.D{
				{Key: "status", Value: "$status"},
				{Key: "server_id", Value: "$metrics.server_id"},
			}},
			{Key: "count", Value: bson.D{{Key: "$sum", Value: 1}}},
		}}},
	}
	opts := options.Aggregate().
		SetHint(r.taskStatusIndex).
		SetAllowDiskUse(false)

	return runStatusServerAgg(ctx, r.db.Collection("tasks"), pipeline, opts)
}

// TasksByCompletedAndServer groups completed tasks by metrics.server_id.
// Intended for infrequent collection (slow_cache_ttl) because completed
// tasks dominate the collection and make the scan O(all tasks).
// Index hint: r.taskStatusIndex (configurable; default iap_status_server_id) {status:1, metrics.server_id:1}
func (r *Runner) TasksByCompletedAndServer(ctx context.Context) ([]StatusServerCount, error) {
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.D{{Key: "status", Value: "complete"}}}},
		{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: bson.D{
				{Key: "status", Value: "$status"},
				{Key: "server_id", Value: "$metrics.server_id"},
			}},
			{Key: "count", Value: bson.D{{Key: "$sum", Value: 1}}},
		}}},
	}
	opts := options.Aggregate().
		SetHint(r.taskStatusIndex).
		SetAllowDiskUse(false)

	return runStatusServerAgg(ctx, r.db.Collection("tasks"), pipeline, opts)
}

// ── Helpers ───────────────────────────────────────────────────────────────────
//
// NOT YET IMPLEMENTED — requires indexes not currently present:
//   TasksAvgRuntime  →  db.tasks.createIndex({"metrics.run_time":1}, {name:"iap_metrics_run_time"})

// groupByField builds a simple $group pipeline that counts documents per value
// of the given field expression (e.g. "$status").
func groupByField(field string) mongo.Pipeline {
	return mongo.Pipeline{
		{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: field},
			{Key: "count", Value: bson.D{{Key: "$sum", Value: 1}}},
		}}},
	}
}

func runStatusServerAgg(
	ctx context.Context,
	coll *mongo.Collection,
	pipeline mongo.Pipeline,
	opts *options.AggregateOptions,
) ([]StatusServerCount, error) {
	cur, err := coll.Aggregate(ctx, pipeline, opts)
	if err != nil {
		return nil, fmt.Errorf("status+server aggregate on %s: %w", coll.Name(), err)
	}
	defer cur.Close(ctx)

	var results []StatusServerCount
	for cur.Next(ctx) {
		var row struct {
			ID struct {
				Status   *string `bson:"status"`
				ServerID *string `bson:"server_id"`
			} `bson:"_id"`
			Count int64 `bson:"count"`
		}
		if err := cur.Decode(&row); err != nil {
			return nil, fmt.Errorf("decoding %s status+server row: %w", coll.Name(), err)
		}
		status := "unknown"
		if row.ID.Status != nil && *row.ID.Status != "" {
			status = *row.ID.Status
		}
		serverID := "unknown"
		if row.ID.ServerID != nil && *row.ID.ServerID != "" {
			serverID = *row.ID.ServerID
		}
		results = append(results, StatusServerCount{
			Status:   status,
			ServerID: serverID,
			Count:    row.Count,
		})
	}
	if err := cur.Err(); err != nil {
		return nil, fmt.Errorf("%s status+server cursor: %w", coll.Name(), err)
	}
	return results, nil
}

func runStatusAgg(
	ctx context.Context,
	coll *mongo.Collection,
	pipeline mongo.Pipeline,
	opts *options.AggregateOptions,
) ([]StatusCount, error) {
	cur, err := coll.Aggregate(ctx, pipeline, opts)
	if err != nil {
		return nil, fmt.Errorf("status aggregate on %s: %w", coll.Name(), err)
	}
	defer cur.Close(ctx)

	var results []StatusCount
	for cur.Next(ctx) {
		var row struct {
			ID    *string `bson:"_id"`
			Count int64   `bson:"count"`
		}
		if err := cur.Decode(&row); err != nil {
			return nil, fmt.Errorf("decoding %s status row: %w", coll.Name(), err)
		}
		status := "unknown"
		if row.ID != nil && *row.ID != "" {
			status = *row.ID
		}
		results = append(results, StatusCount{
			Status: status,
			Count:  row.Count,
		})
	}
	if err := cur.Err(); err != nil {
		return nil, fmt.Errorf("%s status cursor: %w", coll.Name(), err)
	}
	return results, nil
}
