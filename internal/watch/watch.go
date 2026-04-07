package watch

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	ignore "github.com/sabhiram/go-gitignore"
)

type Op string

const (
	Create Op = "create"
	Write  Op = "write"
	Remove Op = "remove"
)

type Event struct {
	Path string
	Op   Op
}

type Watcher struct {
	fsw     *fsnotify.Watcher
	gi      *ignore.GitIgnore
	rootDir string
	events  chan Event
	done    chan struct{}

	mu     sync.Mutex
	timers map[string]*time.Timer
}

func New(dir string, gi *ignore.GitIgnore) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	w := &Watcher{
		fsw:     fsw,
		gi:      gi,
		rootDir: dir,
		events:  make(chan Event, 256),
		done:    make(chan struct{}),
		timers:  make(map[string]*time.Timer),
	}

	if err := w.addRecursive(dir); err != nil {
		fsw.Close()
		return nil, err
	}

	go w.loop()

	return w, nil
}

func (w *Watcher) Events() <-chan Event {
	return w.events
}

func (w *Watcher) Close() error {
	close(w.done)
	return w.fsw.Close()
}

func (w *Watcher) addRecursive(dir string) error {
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if path != dir {
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			rel, err := filepath.Rel(w.rootDir, path)
			if err != nil {
				return filepath.SkipDir
			}
			if w.gi != nil && w.gi.MatchesPath(rel) {
				return filepath.SkipDir
			}
		}
		return w.fsw.Add(path)
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
		case <-w.fsw.Errors:
		}
	}
}

func (w *Watcher) handleEvent(event fsnotify.Event) {
	path := event.Name

	rel, err := filepath.Rel(w.rootDir, path)
	if err != nil {
		return
	}

	if w.gi != nil && w.gi.MatchesPath(rel) {
		return
	}

	base := filepath.Base(path)
	if len(base) > 0 && base[0] == '.' {
		return
	}

	if event.Has(fsnotify.Create) {
		info, err := os.Stat(path)
		if err != nil {
			return
		}
		if info.IsDir() {
			w.fsw.Add(path)
			return
		}
		w.debounce(path, Create)
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
		w.debounce(path, Write)
		return
	}

	if event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename) {
		w.debounce(path, Remove)
	}
}

func (w *Watcher) debounce(path string, op Op) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if t, ok := w.timers[path]; ok {
		t.Stop()
	}

	w.timers[path] = time.AfterFunc(500*time.Millisecond, func() {
		w.mu.Lock()
		delete(w.timers, path)
		w.mu.Unlock()

		select {
		case w.events <- Event{Path: path, Op: op}:
		default:
		}
	})
}
