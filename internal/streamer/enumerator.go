package streamer

import "context"

// Enumerator lists cameras attached to the host. Implementations are
// platform-specific; the windows build uses ffmpeg's `-list_devices`, the
// other build is a development fake.
type Enumerator interface {
	List(ctx context.Context) ([]Camera, error)
}
