package app

import (
	"fmt"
	"testing"
	"time"
)

func TestLiveQueueSizeForWorkers(t *testing.T) {
	tests := []struct {
		workers int
		want    int
	}{
		{0, 16},
		{1, 16},
		{2, 16},
		{4, 32},
		{64, 512},
		{100, 512},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("workers_%d", tt.workers), func(t *testing.T) {
			if got := LiveQueueSizeForWorkers(tt.workers); got != tt.want {
				t.Errorf("LiveQueueSizeForWorkers(%d) = %d, want %d", tt.workers, got, tt.want)
			}
		})
	}
}

func TestRetrySchedulerEvictOverflow(t *testing.T) {
	origDelay := IndexRetryBaseDelay
	IndexRetryBaseDelay = time.Millisecond
	defer func() { IndexRetryBaseDelay = origDelay }()

	rs := NewRetryScheduler()

	for i := 0; i < maxRetryStates+10; i++ {
		path := fmt.Sprintf("/path/%d", i)
		result := rs.Schedule(path, time.Now(), func(retryModTime time.Time) {})
		if result != RetryScheduleScheduled {
			t.Fatalf("expected RetryScheduleScheduled for path %s, got %v", path, result)
		}
	}

	rs.mu.Lock()
	count := len(rs.states)
	rs.mu.Unlock()

	if count > maxRetryStates+1 {
		t.Errorf("expected at most %d states after eviction, got %d", maxRetryStates+1, count)
	}
}

func TestRetrySchedulerGaveUp(t *testing.T) {
	origDelay := IndexRetryBaseDelay
	IndexRetryBaseDelay = time.Millisecond
	defer func() { IndexRetryBaseDelay = origDelay }()

	rs := NewRetryScheduler()
	path := "/test/path.txt"
	modTime := time.Now()

	for i := 0; i < MaxIndexRetryAttempts; i++ {
		result := rs.Schedule(path, modTime, func(retryModTime time.Time) {})
		if result != RetryScheduleScheduled {
			t.Fatalf("expected RetryScheduleScheduled on attempt %d, got %v", i+1, result)
		}
		time.Sleep(time.Duration(i+1)*IndexRetryBaseDelay + 50*time.Millisecond)
	}

	result := rs.Schedule(path, modTime, func(retryModTime time.Time) {})
	if result != RetryScheduleGaveUp {
		t.Errorf("expected RetryScheduleGaveUp, got %v", result)
	}
}
