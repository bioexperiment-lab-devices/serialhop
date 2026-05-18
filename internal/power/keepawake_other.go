//go:build !windows

package power

import "sync/atomic"

type fakeKeepAwake struct {
	active atomic.Bool
}

func newPlatform() (KeepAwake, error) {
	return &fakeKeepAwake{}, nil
}

func (f *fakeKeepAwake) Enable(_ string) error { f.active.Store(true); return nil }
func (f *fakeKeepAwake) Disable() error        { f.active.Store(false); return nil }
func (f *fakeKeepAwake) Active() bool          { return f.active.Load() }
func (f *fakeKeepAwake) Close() error          { f.active.Store(false); return nil }
