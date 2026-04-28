package winsvc

import "errors"

// ServiceState is the platform-neutral set of service states the panel and
// SCM-action code operate on. It maps 1:1 to a subset of svc.State on Windows.
type ServiceState int

const (
	StateNotInstalled ServiceState = iota
	StateStopped
	StateStartPending
	StateRunning
	StateStopPending
)

func (s ServiceState) String() string {
	switch s {
	case StateNotInstalled:
		return "Not installed"
	case StateStopped:
		return "Stopped"
	case StateStartPending:
		return "Starting"
	case StateRunning:
		return "Running"
	case StateStopPending:
		return "Stopping"
	default:
		return "Unknown"
	}
}

// ServiceConfig is the configuration we pass to CreateService. Mapped to
// mgr.Config + extras on Windows.
type ServiceConfig struct {
	DisplayName      string
	Description      string
	BinaryPath       string
	AutoStart        bool   // true → SERVICE_AUTO_START, false → SERVICE_DEMAND_START
	ServiceStartName string // empty → LocalSystem
}

// SCMConn is a connection to the Windows Service Control Manager, abstracted
// for testability.
type SCMConn interface {
	Disconnect() error
	OpenService(name string) (SCMService, error)
	CreateService(name string, cfg ServiceConfig) (SCMService, error)
}

// SCMService is a handle to a single service.
type SCMService interface {
	Query() (ServiceState, error)
	Start() error
	Stop() error
	Delete() error
	Close() error
}

// Sentinel errors returned by SCMConn implementations and surfaced as friendly
// messages by RunAdminAction.
var (
	ErrServiceMissing = errors.New("service is not installed")
	ErrServiceExists  = errors.New("service is already installed")
)

// DialSCM opens a real connection to the Windows SCM with full access.
// Requires admin privileges. Used by the elevated admin-action subprocess
// (install/uninstall/restart). Defined per-platform (real on windows, stub
// elsewhere). Tests inject their own SCMConn instead of going through this.
func DialSCM() (SCMConn, error) {
	return dialSCM()
}

// DialSCMReadOnly opens a low-privilege SCM connection sufficient for
// querying service status. Works without admin elevation. Used by the panel's
// 1 s polling loop, which runs unelevated. CreateService / Start / Stop /
// Delete on the returned connection or services will fail with "access
// denied" — the panel never calls them; admin actions go through the
// elevated subprocess.
func DialSCMReadOnly() (SCMConn, error) {
	return dialSCMReadOnly()
}
