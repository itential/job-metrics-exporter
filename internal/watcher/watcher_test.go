package watcher

import (
	"io"
	"log/slog"
	"testing"

	"go.mongodb.org/mongo-driver/bson"

	"github.com/itential/job-metrics-exporter/internal/config"
)

func newTestWatcher(rec *mockRecorder) *Watcher {
	return New(nil, rec, nil, config.ChangeStreamConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func marshalEvent(t *testing.T, doc bson.D) bson.Raw {
	t.Helper()
	raw, err := bson.Marshal(doc)
	if err != nil {
		t.Fatalf("bson.Marshal: %v", err)
	}
	return raw
}

// ── Job event tests ───────────────────────────────────────────────────────────

func TestHandleJobEvent_Insert(t *testing.T) {
	rec := newMockRecorder()
	w := newTestWatcher(rec)
	w.handleJobEvent(marshalEvent(t, bson.D{{Key: "operationType", Value: "insert"}}))
	if rec.jobStarts != 1 {
		t.Errorf("jobStarts: got %d, want 1", rec.jobStarts)
	}
}

func TestHandleJobEvent_UpdateComplete(t *testing.T) {
	rec := newMockRecorder()
	w := newTestWatcher(rec)
	w.handleJobEvent(marshalEvent(t, bson.D{
		{Key: "operationType", Value: "update"},
		{Key: "updateDescription", Value: bson.D{
			{Key: "updatedFields", Value: bson.D{{Key: "status", Value: "complete"}}},
		}},
	}))
	if rec.jobCompletes != 1 {
		t.Errorf("jobCompletes: got %d, want 1", rec.jobCompletes)
	}
}

func TestHandleJobEvent_UpdateError(t *testing.T) {
	rec := newMockRecorder()
	w := newTestWatcher(rec)
	w.handleJobEvent(marshalEvent(t, bson.D{
		{Key: "operationType", Value: "update"},
		{Key: "updateDescription", Value: bson.D{
			{Key: "updatedFields", Value: bson.D{{Key: "status", Value: "error"}}},
		}},
	}))
	if rec.jobErrors != 1 {
		t.Errorf("jobErrors: got %d, want 1", rec.jobErrors)
	}
}

func TestHandleJobEvent_UpdateCanceled(t *testing.T) {
	rec := newMockRecorder()
	w := newTestWatcher(rec)
	w.handleJobEvent(marshalEvent(t, bson.D{
		{Key: "operationType", Value: "update"},
		{Key: "updateDescription", Value: bson.D{
			{Key: "updatedFields", Value: bson.D{{Key: "status", Value: "canceled"}}},
		}},
	}))
	if rec.jobCancels != 1 {
		t.Errorf("jobCancels: got %d, want 1", rec.jobCancels)
	}
}

func TestHandleJobEvent_ReplaceComplete(t *testing.T) {
	rec := newMockRecorder()
	w := newTestWatcher(rec)
	w.handleJobEvent(marshalEvent(t, bson.D{
		{Key: "operationType", Value: "replace"},
		{Key: "fullDocument", Value: bson.D{{Key: "status", Value: "complete"}}},
	}))
	if rec.jobCompletes != 1 {
		t.Errorf("jobCompletes: got %d, want 1", rec.jobCompletes)
	}
}

func TestHandleJobEvent_ReplaceError(t *testing.T) {
	rec := newMockRecorder()
	w := newTestWatcher(rec)
	w.handleJobEvent(marshalEvent(t, bson.D{
		{Key: "operationType", Value: "replace"},
		{Key: "fullDocument", Value: bson.D{{Key: "status", Value: "error"}}},
	}))
	if rec.jobErrors != 1 {
		t.Errorf("jobErrors: got %d, want 1", rec.jobErrors)
	}
}

func TestHandleJobEvent_ReplaceCanceled(t *testing.T) {
	rec := newMockRecorder()
	w := newTestWatcher(rec)
	w.handleJobEvent(marshalEvent(t, bson.D{
		{Key: "operationType", Value: "replace"},
		{Key: "fullDocument", Value: bson.D{{Key: "status", Value: "canceled"}}},
	}))
	if rec.jobCancels != 1 {
		t.Errorf("jobCancels: got %d, want 1", rec.jobCancels)
	}
}

func TestHandleJobEvent_UnknownOperationType(t *testing.T) {
	rec := newMockRecorder()
	w := newTestWatcher(rec)
	w.handleJobEvent(marshalEvent(t, bson.D{{Key: "operationType", Value: "delete"}}))
	if rec.jobStarts+rec.jobCompletes+rec.jobErrors+rec.jobCancels != 0 {
		t.Error("unexpected recorder calls for unknown operationType")
	}
}

func TestHandleJobEvent_MalformedBSON(t *testing.T) {
	rec := newMockRecorder()
	w := newTestWatcher(rec)
	// Truncated BSON should not panic — error is logged and ignored.
	w.handleJobEvent(bson.Raw{0x05, 0x00, 0x00, 0x00, 0x00}) // minimal valid empty doc
	if rec.jobStarts+rec.jobCompletes+rec.jobErrors+rec.jobCancels != 0 {
		t.Error("unexpected recorder calls for empty BSON document")
	}
}

// ── Task event tests ──────────────────────────────────────────────────────────

func TestHandleTaskEvent_Insert(t *testing.T) {
	rec := newMockRecorder()
	w := newTestWatcher(rec)
	w.handleTaskEvent(marshalEvent(t, bson.D{
		{Key: "operationType", Value: "insert"},
		{Key: "fullDocument", Value: bson.D{
			{Key: "metrics", Value: bson.D{{Key: "server_id", Value: "srv-1"}}},
		}},
	}))
	if rec.taskStarts["srv-1"] != 1 {
		t.Errorf("taskStarts[srv-1]: got %d, want 1", rec.taskStarts["srv-1"])
	}
}

func TestHandleTaskEvent_UpdateComplete(t *testing.T) {
	rec := newMockRecorder()
	w := newTestWatcher(rec)
	w.handleTaskEvent(marshalEvent(t, bson.D{
		{Key: "operationType", Value: "update"},
		{Key: "fullDocument", Value: bson.D{
			{Key: "metrics", Value: bson.D{{Key: "server_id", Value: "srv-1"}}},
		}},
		{Key: "updateDescription", Value: bson.D{
			{Key: "updatedFields", Value: bson.D{{Key: "status", Value: "complete"}}},
		}},
	}))
	if rec.taskCompletes["srv-1"] != 1 {
		t.Errorf("taskCompletes[srv-1]: got %d, want 1", rec.taskCompletes["srv-1"])
	}
}

func TestHandleTaskEvent_UpdateError(t *testing.T) {
	rec := newMockRecorder()
	w := newTestWatcher(rec)
	w.handleTaskEvent(marshalEvent(t, bson.D{
		{Key: "operationType", Value: "update"},
		{Key: "fullDocument", Value: bson.D{
			{Key: "metrics", Value: bson.D{{Key: "server_id", Value: "srv-1"}}},
		}},
		{Key: "updateDescription", Value: bson.D{
			{Key: "updatedFields", Value: bson.D{{Key: "status", Value: "error"}}},
		}},
	}))
	if rec.taskErrors["srv-1"] != 1 {
		t.Errorf("taskErrors[srv-1]: got %d, want 1", rec.taskErrors["srv-1"])
	}
}

func TestHandleTaskEvent_UpdateCanceled(t *testing.T) {
	rec := newMockRecorder()
	w := newTestWatcher(rec)
	w.handleTaskEvent(marshalEvent(t, bson.D{
		{Key: "operationType", Value: "update"},
		{Key: "fullDocument", Value: bson.D{
			{Key: "metrics", Value: bson.D{{Key: "server_id", Value: "srv-1"}}},
		}},
		{Key: "updateDescription", Value: bson.D{
			{Key: "updatedFields", Value: bson.D{{Key: "status", Value: "canceled"}}},
		}},
	}))
	if rec.taskCancels["srv-1"] != 1 {
		t.Errorf("taskCancels[srv-1]: got %d, want 1", rec.taskCancels["srv-1"])
	}
}

func TestHandleTaskEvent_ReplaceComplete(t *testing.T) {
	rec := newMockRecorder()
	w := newTestWatcher(rec)
	w.handleTaskEvent(marshalEvent(t, bson.D{
		{Key: "operationType", Value: "replace"},
		{Key: "fullDocument", Value: bson.D{
			{Key: "status", Value: "complete"},
			{Key: "metrics", Value: bson.D{{Key: "server_id", Value: "srv-2"}}},
		}},
	}))
	if rec.taskCompletes["srv-2"] != 1 {
		t.Errorf("taskCompletes[srv-2]: got %d, want 1", rec.taskCompletes["srv-2"])
	}
}

func TestHandleTaskEvent_ReplaceError(t *testing.T) {
	rec := newMockRecorder()
	w := newTestWatcher(rec)
	w.handleTaskEvent(marshalEvent(t, bson.D{
		{Key: "operationType", Value: "replace"},
		{Key: "fullDocument", Value: bson.D{
			{Key: "status", Value: "error"},
			{Key: "metrics", Value: bson.D{{Key: "server_id", Value: "srv-2"}}},
		}},
	}))
	if rec.taskErrors["srv-2"] != 1 {
		t.Errorf("taskErrors[srv-2]: got %d, want 1", rec.taskErrors["srv-2"])
	}
}

func TestHandleTaskEvent_ReplaceCanceled(t *testing.T) {
	rec := newMockRecorder()
	w := newTestWatcher(rec)
	w.handleTaskEvent(marshalEvent(t, bson.D{
		{Key: "operationType", Value: "replace"},
		{Key: "fullDocument", Value: bson.D{
			{Key: "status", Value: "canceled"},
			{Key: "metrics", Value: bson.D{{Key: "server_id", Value: "srv-2"}}},
		}},
	}))
	if rec.taskCancels["srv-2"] != 1 {
		t.Errorf("taskCancels[srv-2]: got %d, want 1", rec.taskCancels["srv-2"])
	}
}

func TestHandleTaskEvent_InsertNoServerID(t *testing.T) {
	rec := newMockRecorder()
	w := newTestWatcher(rec)
	// Insert with no metrics.server_id — serverID defaults to empty string.
	w.handleTaskEvent(marshalEvent(t, bson.D{{Key: "operationType", Value: "insert"}}))
	if rec.taskStarts[""] != 1 {
		t.Errorf(`taskStarts[""]: got %d, want 1`, rec.taskStarts[""])
	}
}

func TestHandleTaskEvent_UnknownOperationType(t *testing.T) {
	rec := newMockRecorder()
	w := newTestWatcher(rec)
	w.handleTaskEvent(marshalEvent(t, bson.D{{Key: "operationType", Value: "delete"}}))
	total := 0
	for _, v := range rec.taskStarts {
		total += v
	}
	for _, v := range rec.taskCompletes {
		total += v
	}
	for _, v := range rec.taskErrors {
		total += v
	}
	for _, v := range rec.taskCancels {
		total += v
	}
	if total != 0 {
		t.Error("unexpected recorder calls for unknown operationType")
	}
}
