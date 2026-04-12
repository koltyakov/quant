package main

import (
	"context"
	"sync"

	runtimestate "github.com/koltyakov/quant/internal/runtime"
)

// ResyncCoordinator manages filesystem resync operations, ensuring only one
// resync runs at a time while coalescing concurrent requests.
type ResyncCoordinator struct {
	mu      sync.Mutex
	running bool
	pending bool

	// Callbacks for lifecycle events
	onStartup func(ctx context.Context) (syncReport, error)
	onResync  func(ctx context.Context) (syncReport, error)
	onState   func(state runtimestate.IndexState, message string)
	onReady   func(ctx context.Context, report syncReport)
}

// ResyncCallbacks configures the coordinator's behavior.
type ResyncCallbacks struct {
	// OnStartup is called during the initial sync (blocking).
	OnStartup func(ctx context.Context) (syncReport, error)
	// OnResync is called during subsequent resyncs.
	OnResync func(ctx context.Context) (syncReport, error)
	// OnState is called to update the index state.
	OnState func(state runtimestate.IndexState, message string)
	// OnReady is called after a successful initial sync.
	OnReady func(ctx context.Context, report syncReport)
}

// NewResyncCoordinator creates a coordinator with the given callbacks.
func NewResyncCoordinator(callbacks ResyncCallbacks) *ResyncCoordinator {
	return &ResyncCoordinator{
		onStartup: callbacks.OnStartup,
		onResync:  callbacks.OnResync,
		onState:   callbacks.OnState,
		onReady:   callbacks.OnReady,
	}
}

// RunInitialSync performs the initial filesystem sync synchronously.
// This blocks until the initial sync completes.
func (rc *ResyncCoordinator) RunInitialSync(ctx context.Context) {
	if !rc.begin() {
		return
	}
	rc.runLoop(ctx, true)
}

// RequestResync schedules an asynchronous resync. If a resync is already
// running, it marks a pending resync to run after the current one completes.
func (rc *ResyncCoordinator) RequestResync(ctx context.Context) {
	if !rc.begin() {
		return
	}
	go rc.runLoop(ctx, false)
}

// begin attempts to start a resync. Returns true if this caller should
// proceed with the resync, false if one is already running (in which case
// a pending resync is marked).
func (rc *ResyncCoordinator) begin() bool {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	if rc.running {
		rc.pending = true
		return false
	}
	rc.running = true
	return true
}

// finish completes the current resync. If retryAllowed is true and there's
// a pending resync, it returns true indicating the loop should continue.
func (rc *ResyncCoordinator) finish(retryAllowed bool) bool {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	if retryAllowed && rc.pending {
		rc.pending = false
		return true
	}

	rc.running = false
	rc.pending = false
	return false
}

// runLoop executes the resync loop, handling initial startup vs subsequent resyncs.
func (rc *ResyncCoordinator) runLoop(ctx context.Context, startup bool) {
	first := startup
	for {
		var report syncReport
		var err error

		if first && rc.onStartup != nil {
			report, err = rc.onStartup(ctx)
		} else if rc.onResync != nil {
			report, err = rc.onResync(ctx)
		}

		if err != nil {
			if ctx.Err() != nil {
				rc.finish(false)
				return
			}
			rc.setState(runtimestate.IndexStateDegraded, "filesystem resync failed; index may be partially stale")
		} else if first {
			rc.handleInitialSyncComplete(ctx, report)
		} else if report.hadIndexFailures {
			rc.setState(runtimestate.IndexStateDegraded, "filesystem resync completed with indexing failures")
		} else {
			rc.setState(runtimestate.IndexStateReady, "filesystem resync complete")
		}

		first = false
		if !rc.finish(true) {
			return
		}
	}
}

// handleInitialSyncComplete processes the completion of the initial sync.
func (rc *ResyncCoordinator) handleInitialSyncComplete(ctx context.Context, report syncReport) {
	if report.hadIndexFailures {
		rc.setState(runtimestate.IndexStateDegraded, "initial scan completed with indexing failures")
	} else {
		rc.setState(runtimestate.IndexStateReady, "initial scan complete")
	}

	if rc.onReady != nil {
		rc.onReady(ctx, report)
	}
}

// setState updates the index state via the callback.
func (rc *ResyncCoordinator) setState(state runtimestate.IndexState, message string) {
	if rc.onState != nil {
		rc.onState(state, message)
	}
}
