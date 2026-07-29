package app

import (
	"context"
	"sync"

	runtimestate "github.com/koltyakov/quant/internal/runtime"
)

// ResyncCoordinator manages filesystem resync operations, ensuring only one
// resync runs at a time while coalescing concurrent requests.
type ResyncCoordinator struct {
	mu          sync.Mutex
	running     bool
	pending     bool
	startupDone bool

	onStartup func(ctx context.Context) (SyncReport, error)
	onResync  func(ctx context.Context) (SyncReport, error)
	onState   func(state runtimestate.IndexState, message string)
	onReady   func(ctx context.Context, report SyncReport)
}

type SyncReport struct {
	HadIndexFailures bool
}

// ResyncCallbacks configures the coordinator's behavior.
type ResyncCallbacks struct {
	OnStartup func(ctx context.Context) (SyncReport, error)
	OnResync  func(ctx context.Context) (SyncReport, error)
	OnState   func(state runtimestate.IndexState, message string)
	OnReady   func(ctx context.Context, report SyncReport)
}

func NewResyncCoordinator(callbacks ResyncCallbacks) *ResyncCoordinator {
	return &ResyncCoordinator{
		onStartup: callbacks.OnStartup,
		onResync:  callbacks.OnResync,
		onState:   callbacks.OnState,
		onReady:   callbacks.OnReady,
	}
}

func (rc *ResyncCoordinator) RunInitialSync(ctx context.Context) {
	if !rc.begin() {
		return
	}
	rc.runLoop(ctx)
}

func (rc *ResyncCoordinator) RequestResync(ctx context.Context) {
	if !rc.begin() {
		return
	}
	go rc.runLoop(ctx)
}

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

// beginStartup reports whether this run is the first (startup) sync.
// The startup path runs exactly once, on the first successful sync,
// regardless of whether it was triggered by RunInitialSync or RequestResync.
func (rc *ResyncCoordinator) beginStartup() bool {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return !rc.startupDone
}

func (rc *ResyncCoordinator) completeStartup() {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.startupDone = true
}

func (rc *ResyncCoordinator) runLoop(ctx context.Context) {
	for {
		startup := rc.beginStartup()

		var report SyncReport
		var err error

		if startup && rc.onStartup != nil {
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
		} else if startup {
			rc.completeStartup()
			rc.handleInitialSyncComplete(ctx, report)
		} else if report.HadIndexFailures {
			rc.setState(runtimestate.IndexStateDegraded, "filesystem resync completed with indexing failures")
		} else {
			rc.setState(runtimestate.IndexStateReady, "filesystem resync complete")
		}

		if !rc.finish(true) {
			return
		}
	}
}

func (rc *ResyncCoordinator) handleInitialSyncComplete(ctx context.Context, report SyncReport) {
	if report.HadIndexFailures {
		rc.setState(runtimestate.IndexStateDegraded, "initial scan completed with indexing failures")
	} else {
		rc.setState(runtimestate.IndexStateReady, "initial scan complete")
	}

	if rc.onReady != nil {
		rc.onReady(ctx, report)
	}
}

func (rc *ResyncCoordinator) setState(state runtimestate.IndexState, message string) {
	if rc.onState != nil {
		rc.onState(state, message)
	}
}
