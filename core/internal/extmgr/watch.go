package extmgr

import (
	"context"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

const debounce = 300 * time.Millisecond

// Watch reloads the store when its directory or a dev binary changes, e.g.
// after `edelweissd ext install` ran in another process or a dev rebuild.
func (m *Manager) Watch(ctx context.Context) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	storeDir := filepath.Clean(m.opts.Dir)
	if err := w.Add(storeDir); err != nil {
		w.Close()
		return err
	}
	devBins := map[string]bool{}
	for _, d := range m.opts.Dev {
		devBins[filepath.Clean(d.Binary)] = true
		if err := w.Add(filepath.Dir(d.Binary)); err != nil {
			m.opts.Logger.Warn("cannot watch dev extension", "binary", d.Binary, "err", err)
		}
	}
	go func() {
		defer w.Close()
		var timer *time.Timer
		var fire <-chan time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				name := filepath.Clean(ev.Name)
				if filepath.Dir(name) != storeDir && !devBins[name] {
					continue
				}
				if timer == nil {
					timer = time.NewTimer(debounce)
				} else {
					timer.Reset(debounce)
				}
				fire = timer.C
			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				m.opts.Logger.Warn("extension watcher error", "err", err)
			case <-fire:
				fire = nil
				if err := m.Load(ctx); err != nil {
					m.opts.Logger.Warn("reloading extensions failed", "err", err)
				}
			}
		}
	}()
	return nil
}
