//go:build windows

package panel

import (
	"context"

	"github.com/bioexperiment-lab-devices/serialhop/internal/api"
	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
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
	// Implemented in Task 10.
	return config.Default()
}

func (a *App) ValidateConfig(_ config.Config) []FieldError {
	// Implemented in Task 10.
	return nil
}

func (a *App) SaveConfig(_ config.Config) SaveResult {
	// Implemented in Task 10.
	return SaveResult{OK: false}
}

func (a *App) VerifyCredentials(_, _, _ string) CredsResult {
	// Implemented in Task 11.
	return CredsResult{Outcome: "ok"}
}

func (a *App) OpenConfigInEditor() error { return nil } // Implemented in Task 10.
func (a *App) OpenLogsFolder() error     { return nil } // Implemented in Task 14.
func (a *App) OpenReleaseNotes() error   { return nil } // Implemented in Task 13.
func (a *App) PickBackupDir() string     { return "" }  // Implemented in Task 10.

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
