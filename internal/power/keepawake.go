// Package power exposes a KeepAwake handle that, while active, prevents
// Windows from idling into sleep or scheduled automatic shutdown. The
// real implementation calls PowerCreateRequest / PowerSetRequest with
// the PowerRequestSystemRequired type; the request is process-bound, so
// the handle's lifetime is bound to the owning process (here, the
// SerialHop service). A non-Windows fake exists so packages that depend
// on this interface compile and test on macOS/Linux.
package power

// KeepAwake holds the OS-level "keep the system awake" request. The
// underlying resource is process-bound; the service owns one instance
// for its entire lifetime.
type KeepAwake interface {
	// Enable activates the keep-awake request. Idempotent: a second
	// Enable while already active is a successful no-op. reason is
	// surfaced in `powercfg /requests` on Windows (ignored on other
	// platforms). It is captured on the first Enable call and reused
	// for the lifetime of the handle.
	Enable(reason string) error

	// Disable clears the keep-awake request. Idempotent.
	Disable() error

	// Active returns the most recent successfully-applied state.
	Active() bool

	// Close releases the underlying handle. After Close, the instance
	// is unusable. Called from service shutdown.
	Close() error
}

// New returns a platform-appropriate KeepAwake. On Windows it allocates
// the underlying PowerRequest handle lazily on first Enable; on other
// platforms it returns a fake that just tracks the flag in memory.
func New() (KeepAwake, error) { return newPlatform() }
