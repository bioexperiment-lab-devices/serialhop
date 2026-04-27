//go:build windows

package panel

import (
	"time"

	"github.com/lxn/walk"
)

type tickTimer struct {
	stop chan struct{}
}

func newTickTimer(mw *walk.MainWindow, interval time.Duration, fn func()) (*tickTimer, error) {
	t := &tickTimer{stop: make(chan struct{})}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				mw.Synchronize(fn)
			case <-t.stop:
				return
			}
		}
	}()
	return t, nil
}

func (t *tickTimer) Dispose() { close(t.stop) }
