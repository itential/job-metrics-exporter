package watcher

type mockRecorder struct {
	jobStarts    int
	jobCompletes int
	jobErrors    int
	jobCancels   int

	taskStarts    map[string]int
	taskCompletes map[string]int
	taskErrors    map[string]int
	taskCancels   map[string]int
}

func newMockRecorder() *mockRecorder {
	return &mockRecorder{
		taskStarts:    make(map[string]int),
		taskCompletes: make(map[string]int),
		taskErrors:    make(map[string]int),
		taskCancels:   make(map[string]int),
	}
}

func (m *mockRecorder) RecordJobStart()              { m.jobStarts++ }
func (m *mockRecorder) RecordJobComplete()           { m.jobCompletes++ }
func (m *mockRecorder) RecordJobError()              { m.jobErrors++ }
func (m *mockRecorder) RecordJobCancel()             { m.jobCancels++ }
func (m *mockRecorder) RecordTaskStart(id string)    { m.taskStarts[id]++ }
func (m *mockRecorder) RecordTaskComplete(id string) { m.taskCompletes[id]++ }
func (m *mockRecorder) RecordTaskError(id string)    { m.taskErrors[id]++ }
func (m *mockRecorder) RecordTaskCancel(id string)   { m.taskCancels[id]++ }

// ApplyJobStatusDelta and ApplyTaskStatusDelta are no-ops in the mock; watcher
// event tests verify counter increments, not gauge deltas.
func (m *mockRecorder) ApplyJobStatusDelta(status string, delta int64)  {}
func (m *mockRecorder) ApplyTaskStatusDelta(status string, delta int64) {}
