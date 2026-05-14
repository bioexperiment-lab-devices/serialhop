//go:build windows

package panel

import "github.com/wailsapp/wails/v2/pkg/runtime"

// emitEvent is a thin wrapper around runtime.EventsEmit that no-ops
// when ctx is nil (i.e. before startup completes — used in early-life
// log lines we don't want to surface).
func (a *App) emitEvent(name string, data interface{}) {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, name, data)
}

func (a *App) emitWarn(msg string) {
	a.emitEvent("warn:set", map[string]string{"message": msg})
}

func (a *App) clearWarn() {
	a.emitEvent("warn:clear", nil)
}
