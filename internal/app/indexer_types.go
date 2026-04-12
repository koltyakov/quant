package app

import (
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/koltyakov/quant/internal/logx"
)

var (
	IndexRetryBaseDelay   = 2 * time.Second
	MaxIndexRetryAttempts = 3
)

const (
	liveQueueMultiplier = 8
	minLiveQueueSize    = 16
	maxLiveQueueSize    = 512
	maxRetryStates      = 256
)

type PathSyncTracker struct {
	mu     sync.Mutex
	states map[string]*pathState
}

type pathState struct {
	running bool
	dirty   bool
	version uint64

	hasModTime   bool
	requestedMod time.Time
}

func NewPathSyncTracker() *PathSyncTracker {
	return &PathSyncTracker{states: make(map[string]*pathState)}
}

func (t *PathSyncTracker) Begin(key string, modTime *time.Time) (uint64, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	state, ok := t.states[key]
	if !ok {
		state = &pathState{}
		t.states[key] = state
	}
	if state.running {
		if t.requestInvalidates(state, modTime) {
			state.version++
			state.dirty = true
			state.hasModTime = modTime != nil
			if modTime != nil {
				state.requestedMod = *modTime
			}
		}
		return state.version, false
	}
	state.version++
	state.running = true
	state.hasModTime = modTime != nil
	if modTime != nil {
		state.requestedMod = *modTime
	}
	return state.version, true
}

func (t *PathSyncTracker) Finish(key string) (uint64, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	state, ok := t.states[key]
	if !ok {
		return 0, false
	}
	if state.dirty {
		state.dirty = false
		return state.version, true
	}
	delete(t.states, key)
	return 0, false
}

func (t *PathSyncTracker) IsCurrent(key string, version uint64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	state, ok := t.states[key]
	return ok && state.running && state.version == version
}

func (t *PathSyncTracker) InvalidatePrefix(prefix string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for key, state := range t.states {
		if key == prefix || strings.HasPrefix(key, prefix+string(filepath.Separator)) {
			state.version++
			state.dirty = true
		}
	}
}

func (t *PathSyncTracker) requestInvalidates(state *pathState, modTime *time.Time) bool {
	if state == nil {
		return true
	}
	if modTime == nil {
		return state.hasModTime
	}
	if !state.hasModTime {
		return true
	}
	return !SameModTime(state.requestedMod, *modTime)
}

type LiveIndexQueue struct {
	mu     sync.Mutex
	Jobs   chan string
	states map[string]*livePathState
}

type livePathState struct {
	modTime    time.Time
	hasPending bool
	queued     bool
	running    bool
}

func NewLiveIndexQueue(queueSize int) *LiveIndexQueue {
	return &LiveIndexQueue{
		Jobs:   make(chan string, queueSize),
		states: make(map[string]*livePathState),
	}
}

func (q *LiveIndexQueue) MarkPending(path string, modTime time.Time) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	state, ok := q.states[path]
	if !ok {
		state = &livePathState{}
		q.states[path] = state
	}
	if !state.hasPending || modTime.After(state.modTime) {
		state.modTime = modTime
	}
	state.hasPending = true
	if state.queued || state.running {
		return false
	}
	state.queued = true
	return true
}

func (q *LiveIndexQueue) Cancel(path string) {
	q.mu.Lock()
	defer q.mu.Unlock()

	state, ok := q.states[path]
	if !ok || !state.queued || state.running {
		return
	}
	state.queued = false
	if !state.running {
		delete(q.states, path)
	}
}

func (q *LiveIndexQueue) StartProcessing(path string) (time.Time, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	state, ok := q.states[path]
	if !ok || !state.queued {
		return time.Time{}, false
	}
	state.queued = false
	state.running = true
	modTime := state.modTime
	state.hasPending = false
	return modTime, true
}

func (q *LiveIndexQueue) FinishProcessing(path string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	state, ok := q.states[path]
	if !ok {
		return false
	}
	state.running = false
	if state.hasPending && !state.queued {
		state.queued = true
		return true
	}
	delete(q.states, path)
	return false
}

type RetryScheduler struct {
	mu     sync.Mutex
	states map[string]*retryState
}

type retryState struct {
	attempts int
	modTime  time.Time
	timer    *time.Timer
}

func NewRetryScheduler() *RetryScheduler {
	return &RetryScheduler{states: make(map[string]*retryState)}
}

func (r *RetryScheduler) Clear(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.states == nil {
		return
	}
	state, ok := r.states[path]
	if !ok {
		return
	}
	if state.timer != nil {
		state.timer.Stop()
	}
	delete(r.states, path)
}

func (r *RetryScheduler) evictOverflow() int {
	if len(r.states) <= maxRetryStates {
		return 0
	}

	evicted := 0
	for path, state := range r.states {
		if evicted >= len(r.states)-maxRetryStates+1 {
			break
		}
		if state.timer != nil {
			state.timer.Stop()
		}
		delete(r.states, path)
		evicted++
	}
	logx.Warn("retry scheduler evicted overflow entries", "count", evicted)
	return evicted
}

func (r *RetryScheduler) Schedule(path string, modTime time.Time, onFire func(retryModTime time.Time)) bool {
	if path == "" {
		return false
	}

	r.mu.Lock()
	if r.states == nil {
		r.states = make(map[string]*retryState)
	}

	if len(r.states) >= maxRetryStates {
		r.evictOverflow()
	}

	state, ok := r.states[path]
	if !ok {
		state = &retryState{}
		r.states[path] = state
	}
	if state.timer != nil {
		if state.modTime.IsZero() || modTime.After(state.modTime) {
			state.modTime = modTime
		}
		r.mu.Unlock()
		return false
	}
	if state.attempts >= MaxIndexRetryAttempts {
		attempts := state.attempts
		delete(r.states, path)
		r.mu.Unlock()
		logx.Warn("giving up retrying path", "path", path, "attempts", attempts)
		return false
	}
	state.attempts++
	if state.modTime.IsZero() || modTime.After(state.modTime) {
		state.modTime = modTime
	}
	delay := time.Duration(state.attempts) * IndexRetryBaseDelay
	attempts := state.attempts
	state.timer = time.AfterFunc(delay, func() {
		r.mu.Lock()
		current, ok := r.states[path]
		if !ok {
			r.mu.Unlock()
			return
		}
		current.timer = nil
		retryModTime := current.modTime
		r.mu.Unlock()

		onFire(retryModTime)
	})
	r.mu.Unlock()

	logx.Warn("retrying path", "path", path, "delay", delay, "attempt", attempts, "max_attempts", MaxIndexRetryAttempts)
	return true
}

func LiveQueueSizeForWorkers(workers int) int {
	if workers < 1 {
		workers = 1
	}
	size := workers * liveQueueMultiplier
	if size < minLiveQueueSize {
		size = minLiveQueueSize
	}
	if size > maxLiveQueueSize {
		size = maxLiveQueueSize
	}
	return size
}
