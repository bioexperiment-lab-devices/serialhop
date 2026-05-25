//go:build windows

package panel

import (
	"context"
	"errors"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/bioexperiment-lab-devices/serialhop/internal/streamer"
)

// ListCameras returns the current streaming snapshot for the Cameras
// tab. Does NOT take context.Context — Wails v2's auto-injection
// doesn't work through main.App embedding, so the binding would fail
// silently.
func (a *App) ListCameras() streamer.StreamingState {
	if a.streaming == nil || a.streaming.Manager() == nil {
		return streamer.StreamingState{}
	}
	m := a.streaming.Manager()
	return streamer.StreamingState{
		Cameras:  m.Cameras(),
		FfmpegOK: ffmpegOK(m),
	}
}

// SetCameraArmed flips the armed bit on a single camera. Emits a
// `streaming:state` Wails event so the UI re-fetches.
func (a *App) SetCameraArmed(id string, armed bool) error {
	if a.streaming == nil || a.streaming.Manager() == nil {
		return errors.New("streaming subsystem not initialized")
	}
	if err := a.streaming.Manager().SetArmed(id, armed); err != nil {
		return err
	}
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "streaming:state")
	}
	return nil
}

// RefreshCameras re-runs enumeration and emits `streaming:state`.
func (a *App) RefreshCameras() error {
	if a.streaming == nil || a.streaming.Manager() == nil {
		return errors.New("streaming subsystem not initialized")
	}
	if _, err := a.streaming.Manager().Refresh(context.Background()); err != nil {
		return err
	}
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "streaming:state")
	}
	return nil
}

// ffmpegOK reports whether the bundled ffmpeg has passed its first
// probe. For v1, we treat the probe as OK until a Start has failed —
// the UI banner appears on persistent failure rather than at startup.
// (See plan Task 12 comment: a richer probe-result-exposure can come
// in v2.)
func ffmpegOK(_ streamer.Manager) bool {
	return true
}
