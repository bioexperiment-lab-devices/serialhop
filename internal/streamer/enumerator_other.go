//go:build !windows

package streamer

import (
	"context"
)

// NewEnumerator returns a development fake on non-Windows hosts.
//
// The fake is intentionally minimal: one canned camera so the panel UI
// renders something in `wails dev` on a developer Mac/Linux box. Tests
// for production behavior (parsing, lifecycle) live in the *_windows*
// files behind a build tag.
func NewEnumerator() Enumerator {
	return fakeEnumerator{}
}

type fakeEnumerator struct{}

func (fakeEnumerator) List(_ context.Context) ([]Camera, error) {
	return []Camera{
		{
			ID:    "fake:dev-camera-0",
			Label: "Fake Dev Camera",
		},
	}, nil
}
