package runtime

import (
	"strings"
	"sync"
	"time"
)

type IndexState string

const (
	IndexStateStarting IndexState = "starting"
	IndexStateIndexing IndexState = "indexing"
	IndexStateReady    IndexState = "ready"
	IndexStateDegraded IndexState = "degraded"
)

type IndexSnapshot struct {
	State     IndexState `json:"state"`
	Message   string     `json:"message,omitempty"`
	UpdatedAt time.Time  `json:"updated_at"`
}

func (s IndexSnapshot) Ready() bool {
	return s.State == IndexStateReady || s.State == IndexStateDegraded
}

type IndexStateTracker struct {
	mu      sync.RWMutex
	state   IndexState
	message string
	updated time.Time
}

func NewIndexStateTracker() *IndexStateTracker {
	now := time.Now().UTC()
	return &IndexStateTracker{
		state:   IndexStateStarting,
		updated: now,
	}
}

func (t *IndexStateTracker) Set(state IndexState, message string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.state = state
	t.message = strings.TrimSpace(message)
	t.updated = time.Now().UTC()
}

func (t *IndexStateTracker) Snapshot() IndexSnapshot {
	if t == nil {
		return IndexSnapshot{}
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return IndexSnapshot{
		State:     t.state,
		Message:   t.message,
		UpdatedAt: t.updated,
	}
}
