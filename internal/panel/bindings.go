//go:build windows

package panel

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"gopkg.in/yaml.v3"

	"github.com/bioexperiment-lab-devices/serialhop/internal/api"
	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
	"github.com/bioexperiment-lab-devices/serialhop/internal/paths"
	"github.com/bioexperiment-lab-devices/serialhop/internal/version"
)

// --- DTOs declared just for the binding surface. ---

// FieldError pairs a config field path (dot-separated for nested structs,
// e.g. "lab_bridge.host") with a human-readable detail string.
type FieldError struct {
	Field  string `json:"field"`
	Detail string `json:"detail"`
}

type SaveResult struct {
	OK          bool         `json:"ok"`
	FieldErrors []FieldError `json:"field_errors,omitempty"`
}

type CredsResult struct {
	Outcome string `json:"outcome"` // "ok" | "unauthorized" | "needs_confirm" | "skipped"
	Detail  string `json:"detail,omitempty"`
}

type AdminResult struct {
	OK           bool   `json:"ok"`
	ErrorMessage string `json:"error_message,omitempty"`
	Cancelled    bool   `json:"cancelled,omitempty"`
}

type ServiceTabStatusDTO struct {
	Reachable bool   `json:"reachable"`
	Reason    string `json:"reason,omitempty"` // "service_down" | "unreachable" | ""
}

// --- Bindings ---

func (a *App) GetVersion() string { return version.Base() }

func (a *App) LoadConfigFromDisk() config.Config {
	p := resolveConfigPath()
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return config.Default()
	}
	c, err := config.Load(p)
	if err != nil {
		a.emitWarn("Config file unreadable: " + err.Error())
		return config.Default()
	}
	return c
}

func (a *App) ValidateConfig(cfg config.Config) []FieldError {
	if err := config.Validate(&cfg); err != nil {
		return []FieldError{{Field: extractField(err), Detail: err.Error()}}
	}
	return nil
}

func (a *App) SaveConfig(cfg config.Config) SaveResult {
	if errs := a.ValidateConfig(cfg); len(errs) > 0 {
		return SaveResult{OK: false, FieldErrors: errs}
	}
	p := resolveConfigPath()
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return SaveResult{OK: false, FieldErrors: []FieldError{{Detail: err.Error()}}}
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		return SaveResult{OK: false, FieldErrors: []FieldError{{Detail: err.Error()}}}
	}
	a.emitEvent("config:saved", nil)
	return SaveResult{OK: true}
}

// VerifyCredentials runs the verify-then-save state machine for the
// CURRENT form vs the on-disk YAML. Returns a categorical outcome the
// TS side maps to inline errors / confirm modals (spec §5.9).
func (a *App) VerifyCredentials(newHost, newUser, newPass string) CredsResult {
	old := a.LoadConfigFromDisk()
	cv := NewCredVerifier(&liveCredVerifier{hc: &http.Client{Timeout: 10 * time.Second}})
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	dec, _ := cv.Decide(ctx, CredChange{
		OldUser: old.LabBridge.User, OldPass: old.LabBridge.Pass,
		NewHost: newHost, NewUser: newUser, NewPass: newPass,
	})
	return CredsResult{Outcome: dec.Outcome, Detail: dec.Detail}
}

func (a *App) OpenConfigInEditor() error {
	return OpenWithDefaultApp(resolveConfigPath())
}

func (a *App) OpenLogsFolder() error   { return nil } // Implemented in Task 14.
func (a *App) OpenReleaseNotes() error { return nil } // Implemented in Task 13.

func (a *App) PickBackupDir() string {
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Choose firmware backup directory",
	})
	if err != nil {
		return ""
	}
	return dir
}

func (a *App) InstallService() AdminResult   { return AdminResult{} } // Implemented in Task 12.
func (a *App) UninstallService() AdminResult { return AdminResult{} } // Implemented in Task 12.
func (a *App) RestartService() AdminResult   { return AdminResult{} } // Implemented in Task 12.

func (a *App) TriggerProbe(_ string) {} // Implemented in Task 16.
func (a *App) CheckForUpdate()       {} // Implemented in Task 13.
func (a *App) DownloadUpdate()       {} // Implemented in Task 13.
func (a *App) CancelDownload()       {} // Implemented in Task 13.
func (a *App) InstallUpdate() AdminResult { // Implemented in Task 13.
	return AdminResult{}
}

func (a *App) GetDevices(_ context.Context) (api.DevicesResponse, ServiceTabStatusDTO) {
	return api.DevicesResponse{}, ServiceTabStatusDTO{Reachable: false, Reason: "unreachable"}
}
func (a *App) Discover(_ context.Context) (api.DevicesResponse, ServiceTabStatusDTO) {
	return api.DevicesResponse{}, ServiceTabStatusDTO{Reachable: false, Reason: "unreachable"}
}
func (a *App) DisconnectAll(_ context.Context) (api.DisconnectResponse, ServiceTabStatusDTO) {
	return api.DisconnectResponse{}, ServiceTabStatusDTO{Reachable: false, Reason: "unreachable"}
}
func (a *App) GetPorts(_ context.Context) (api.DetailedPortsResponse, ServiceTabStatusDTO) {
	return api.DetailedPortsResponse{}, ServiceTabStatusDTO{Reachable: false, Reason: "unreachable"}
}

func (a *App) StartLogStream(_ string) {} // Implemented in Task 14.
func (a *App) StopLogStream()          {} // Implemented in Task 14.

// --- Helpers ---

// resolveConfigPath returns paths.ConfigPath() unless the test hook is
// set (SERIALHOP_TEST_CONFIG_PATH). Used to make config bindings
// unit-testable without touching ProgramData.
func resolveConfigPath() string {
	if p := os.Getenv("SERIALHOP_TEST_CONFIG_PATH"); p != "" {
		return p
	}
	return paths.ConfigPath()
}

// extractField pulls a dot-path out of a config.Validate error when one is
// present. config.Validate returns errors like "lab_bridge.host must be
// non-empty"; the first space-delimited token is the field path if it
// contains a dot and no spaces itself. Falls back to empty string (which
// the UI maps to a global banner).
func extractField(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	// Try colon-separated format first ("lab_bridge.host: required").
	if idx := strings.Index(msg, ":"); idx > 0 {
		candidate := msg[:idx]
		if !strings.ContainsAny(candidate, " ") {
			return candidate
		}
	}
	// Fall back to space-separated format ("lab_bridge.host must be ...").
	if idx := strings.IndexByte(msg, ' '); idx > 0 {
		candidate := msg[:idx]
		if strings.ContainsRune(candidate, '.') {
			return candidate
		}
	}
	return ""
}

// liveCredVerifier adapts the existing verifyCredentials helper in
// firstrun.go to the CredVerifier interface so CredVerify can drive it.
type liveCredVerifier struct {
	hc *http.Client
}

func (l *liveCredVerifier) Verify(ctx context.Context, host, user, pass string) (CredsCheckKind, error) {
	base := ""
	if host != "" {
		base = "https://" + host
	}
	userAgent := "SerialHop/" + version.Base() + " (panel)"
	res := verifyCredentials(ctx, l.hc, base, user, pass, userAgent)
	if res.Detail != "" {
		return res.Kind, errors.New(res.Detail)
	}
	return res.Kind, nil
}
