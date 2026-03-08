package watcher

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ReloadFunc is called when a config file change is detected.
type ReloadFunc func()

// Watcher monitors config file changes and triggers reload.
type Watcher struct {
	logger   *slog.Logger
	path     string // absolute path to config file
	reload   ReloadFunc
	debounce time.Duration

	mu      sync.Mutex
	stopped bool
}

// New creates a config file watcher.
// path must be a resolved absolute path to the config file.
// debounce prevents rapid successive reloads (e.g. editors doing write+rename).
func New(logger *slog.Logger, path string, reload ReloadFunc, debounce time.Duration) *Watcher {
	if debounce <= 0 {
		debounce = 500 * time.Millisecond
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return &Watcher{
		logger:   logger,
		path:     abs,
		reload:   reload,
		debounce: debounce,
	}
}

// Run blocks until ctx is cancelled. It watches the config file's parent
// directory (to handle atomic renames used by many editors) and fires reload
// on write/create/rename events targeting the config file.
func (w *Watcher) Run(ctx context.Context) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		w.logger.Error("config watcher: failed to create fsnotify watcher", "error", err)
		return
	}
	defer fsw.Close()

	dir := filepath.Dir(w.path)
	base := filepath.Base(w.path)

	if err := fsw.Add(dir); err != nil {
		w.logger.Error("config watcher: failed to watch directory", "dir", dir, "error", err)
		return
	}
	w.logger.Info("config watcher started", "file", w.path, "dir", dir)

	var (
		timer   *time.Timer
		timerMu sync.Mutex
		pending bool
	)

	resetTimer := func() {
		timerMu.Lock()
		defer timerMu.Unlock()
		if timer != nil {
			timer.Stop()
		}
		pending = true
		timer = time.AfterFunc(w.debounce, func() {
			timerMu.Lock()
			pending = false
			timerMu.Unlock()

			w.mu.Lock()
			stopped := w.stopped
			w.mu.Unlock()
			if stopped {
				return
			}

			w.logger.Info("config file changed, reloading")
			w.reload()
		})
	}

	for {
		select {
		case <-ctx.Done():
			w.mu.Lock()
			w.stopped = true
			w.mu.Unlock()
			timerMu.Lock()
			if timer != nil {
				timer.Stop()
			}
			_ = pending
			timerMu.Unlock()
			w.logger.Info("config watcher stopped")
			return

		case event, ok := <-fsw.Events:
			if !ok {
				return
			}
			// Only react to changes on the config file itself
			if filepath.Base(event.Name) != base {
				continue
			}
			// Write, Create, Rename all indicate potential config change
			if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) || event.Has(fsnotify.Rename) {
				resetTimer()
			}

		case err, ok := <-fsw.Errors:
			if !ok {
				return
			}
			w.logger.Warn("config watcher error", "error", err)
		}
	}
}
