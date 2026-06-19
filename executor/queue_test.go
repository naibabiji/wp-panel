package executor

import (
	"testing"
	"time"
)

func TestQueueStoresTaskResultForPolling(t *testing.T) {
	q := InitQueue(nil)
	task := q.Enqueue(TaskType("unknown_for_test"), nil)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, ok := q.GetTask(task.ID)
		if !ok {
			t.Fatal("queued task not found")
		}
		if got.Result != nil {
			if got.Status != TaskStatusFailed {
				t.Fatalf("status = %q, want %q", got.Status, TaskStatusFailed)
			}
			if got.Result.Success {
				t.Fatal("result success = true, want false")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for queued task result")
}
