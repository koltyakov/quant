package watch

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/koltyakov/quant/internal/logx"
	"github.com/koltyakov/quant/internal/scan"
	ignore "github.com/sabhiram/go-gitignore"
)

type Op string

const (
	Create Op = "create"
	Write  Op = "write"
	Remove Op = "remove"
	Resync Op = "resync"
)

type Event struct {
	Path  string
	Op    Op
	IsDir bool
}

type Options struct {
	EventBuffer int
}

type Watcher struct {
	fsw     *fsnotify.Watcher
	matcher *scan.GitIgnoreMatcher
	rootDir string
	events  chan Event
	done    chan struct{}

	mu            sync.Mutex
	timers        map[string]*time.Timer
	resyncTimer   *time.Timer
	watchedDirs   map[string]struct{}
	resyncPending bool
	closed        bool
}

const (
	defaultEventBufferSize  = 256
	defaultDebounceDelay    = 500 * time.Millisecond
	defaultResyncRetryDelay = 500 * time.Millisecond
)

func New(dir string, gi *ignore.GitIgnore, opts ...Options) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	cfg := Options{EventBuffer: defaultEventBufferSize}
	if len(opts) > 0 && opts[0].EventBuffer > 0 {
		cfg.EventBuffer = opts[0].EventBuffer
	}

	w := &Watcher{
		fsw:     fsw,
		matcher: scan.NewGitIgnoreMatcher(dir, gi),
		rootDir: dir,
		events:  make(chan Event, cfg.EventBuffer),
		done:    make(chan struct{}),
		timers:  make(map[string]*time.Timer),
		watchedDirs: map[string]struct{}{
			dir: {},
		},
	}

	if err := w.addRecursive(dir); err != nil {
		_ = fsw.Close()
		return nil, err
	}

	go w.loop()

	return w, nil
}

func (w *Watcher) Events() <-chan Event {
	return w.events
}

func (w *Watcher) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	for path, timer := range w.timers {
		timer.Stop()
		delete(w.timers, path)
	}
	if w.resyncTimer != nil {
		w.resyncTimer.Stop()
		w.resyncTimer = nil
	}
	close(w.done)
	w.mu.Unlock()
	return w.fsw.Close()
}

func (w *Watcher) addRecursive(dir string) error {
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			logx.Warn("watcher could not descend into path", "path", path, "err", err)
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if path != w.rootDir {
			if scan.IsHiddenName(d.Name()) {
				return filepath.SkipDir
			}
			if w.matcher.Matches(path) {
				return filepath.SkipDir
			}
			w.matcher.Load(path)
		}
		if err := w.fsw.Add(path); err != nil {
			return err
		}
		w.mu.Lock()
		w.watchedDirs[path] = struct{}{}
		w.mu.Unlock()
		return nil
	})
}

func (w *Watcher) loop() {
	for {
		select {
		case <-w.done:
			return
		case event, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			w.handleEvent(event)
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			w.handleBackendError(err)
		}
	}
}

func (w *Watcher) handleBackendError(err error) {
	if err != nil {
		logx.Warn("watcher backend error", "err", err)
	}
	w.signalResync()
}

func (w *Watcher) handleEvent(event fsnotify.Event) {
	path := event.Name

	base := filepath.Base(path)
	if base == ".gitignore" {
		w.handleGitIgnoreEvent(path)
		return
	}

	if w.matcher.Matches(path) {
		return
	}

	if event.Has(fsnotify.Create) {
		info, err := os.Stat(path)
		if err != nil {
			return
		}
		if info.IsDir() {
			if scan.IsHiddenName(base) {
				return
			}
			w.matcher.Load(path)
			if err := w.addRecursive(path); err != nil {
				logx.Warn("watcher failed to add recursive directory", "path", path, "err", err)
				w.signalResync()
				return
			}
			w.signalResync()
			return
		}
		w.debounce(path, Create, false)
		return
	}

	if event.Has(fsnotify.Write) {
		info, err := os.Stat(path)
		if err != nil {
			return
		}
		if info.IsDir() {
			return
		}
		w.debounce(path, Write, false)
		return
	}

	if event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename) {
		isDir := w.isWatchedDir(path)
		if isDir && scan.IsHiddenName(base) {
			return
		}
		if isDir {
			w.matcher.Remove(path)
		}
		w.debounce(path, Remove, isDir)
	}
}

func (w *Watcher) handleGitIgnoreEvent(path string) {
	dir := filepath.Dir(path)
	w.matcher.Reload(dir)
	w.signalResync()
}

func (w *Watcher) isWatchedDir(path string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	_, ok := w.watchedDirs[path]
	if ok {
		delete(w.watchedDirs, path)
		for watched := range w.watchedDirs {
			if strings.HasPrefix(watched, path+string(filepath.Separator)) {
				delete(w.watchedDirs, watched)
			}
		}
	}
	return ok
}

func (w *Watcher) debounce(path string, op Op, isDir bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}

	if t, ok := w.timers[path]; ok {
		t.Stop()
	}

	w.timers[path] = time.AfterFunc(defaultDebounceDelay, func() {
		w.mu.Lock()
		delete(w.timers, path)
		closed := w.closed
		w.mu.Unlock()
		if closed {
			return
		}

		select {
		case <-w.done:
			return
		case w.events <- Event{Path: path, Op: op, IsDir: isDir}:
		default:
			logx.Warn("watcher event dropped", "path", path, "reason", "channel full")
			w.signalResync()
		}
	})
}

func (w *Watcher) signalResync() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	if w.resyncPending {
		w.mu.Unlock()
		return
	}
	w.resyncPending = true
	w.mu.Unlock()

	w.trySendResync()
}

func (w *Watcher) trySendResync() {
	select {
	case <-w.done:
		return
	case w.events <- Event{Path: w.rootDir, Op: Resync, IsDir: true}:
		w.mu.Lock()
		w.resyncPending = false
		w.resyncTimer = nil
		w.mu.Unlock()
	default:
		w.mu.Lock()
		if w.closed {
			w.mu.Unlock()
			return
		}
		if w.resyncTimer != nil {
			w.resyncTimer.Stop()
		}
		w.resyncTimer = time.AfterFunc(defaultResyncRetryDelay, func() {
			w.mu.Lock()
			pending := w.resyncPending
			closed := w.closed
			w.resyncTimer = nil
			w.mu.Unlock()
			if pending && !closed {
				w.trySendResync()
			}
		})
		w.mu.Unlock()
	}
}
