package app

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
	rc.runLoop(ctx, true)
}

func (rc *ResyncCoordinator) RequestResync(ctx context.Context) {
	if !rc.begin() {
		return
	}
	go rc.runLoop(ctx, false)
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

func (rc *ResyncCoordinator) runLoop(ctx context.Context, startup bool) {
	first := startup
	for {
		var report SyncReport
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
		} else if report.HadIndexFailures {
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
