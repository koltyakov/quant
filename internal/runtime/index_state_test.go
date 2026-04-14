package runtime

import (
	"testing"
	"time"
)

func TestIndexSnapshotReady(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state IndexState
		want  bool
	}{
		{name: "starting", state: IndexStateStarting, want: false},
		{name: "indexing", state: IndexStateIndexing, want: false},
		{name: "ready", state: IndexStateReady, want: true},
		{name: "degraded", state: IndexStateDegraded, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			snapshot := IndexSnapshot{State: tt.state}
			if got := snapshot.Ready(); got != tt.want {
				t.Fatalf("Ready() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestNewIndexStateTrackerStartsInStartingState(t *testing.T) {
	t.Parallel()

	tracker := NewIndexStateTracker()
	snapshot := tracker.Snapshot()

	if snapshot.State != IndexStateStarting {
		t.Fatalf("Snapshot().State = %q, want %q", snapshot.State, IndexStateStarting)
	}
	if snapshot.Message != "" {
		t.Fatalf("Snapshot().Message = %q, want empty", snapshot.Message)
	}
	if snapshot.UpdatedAt.IsZero() {
		t.Fatal("Snapshot().UpdatedAt was zero")
	}
}

func TestIndexStateTrackerSetTrimsMessageAndUpdatesTimestamp(t *testing.T) {
	t.Parallel()

	tracker := NewIndexStateTracker()
	before := tracker.Snapshot().UpdatedAt
	time.Sleep(10 * time.Millisecond)

	tracker.Set(IndexStateReady, "  fully indexed  ")
	snapshot := tracker.Snapshot()

	if snapshot.State != IndexStateReady {
		t.Fatalf("Snapshot().State = %q, want %q", snapshot.State, IndexStateReady)
	}
	if snapshot.Message != "fully indexed" {
		t.Fatalf("Snapshot().Message = %q, want %q", snapshot.Message, "fully indexed")
	}
	if !snapshot.UpdatedAt.After(before) {
		t.Fatalf("Snapshot().UpdatedAt = %v, want after %v", snapshot.UpdatedAt, before)
	}
}

func TestIndexStateTrackerNilReceiverIsSafe(t *testing.T) {
	t.Parallel()

	var tracker *IndexStateTracker
	tracker.Set(IndexStateDegraded, "ignored")

	snapshot := tracker.Snapshot()
	if snapshot.State != "" {
		t.Fatalf("Snapshot().State = %q, want empty", snapshot.State)
	}
	if snapshot.Message != "" {
		t.Fatalf("Snapshot().Message = %q, want empty", snapshot.Message)
	}
	if !snapshot.UpdatedAt.IsZero() {
		t.Fatalf("Snapshot().UpdatedAt = %v, want zero", snapshot.UpdatedAt)
	}
}
