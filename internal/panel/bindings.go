//go:build windows

package panel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"gopkg.in/yaml.v3"

	"github.com/bioexperiment-lab-devices/serialhop/internal/api"
	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
	"github.com/bioexperiment-lab-devices/serialhop/internal/paths"
	"github.com/bioexperiment-lab-devices/serialhop/internal/updater"
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

func (a *App) OpenLogsFolder() error {
	return OpenWithDefaultApp(paths.LogsDir())
}

func (a *App) OpenReleaseNotes() error {
	a.updateCh.mu.Lock()
	url := a.updateCh.release.HTMLURL
	a.updateCh.mu.Unlock()
	if url == "" {
		return nil
	}
	return OpenWithDefaultApp(url)
}

func (a *App) PickBackupDir() string {
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Choose firmware backup directory",
	})
	if err != nil {
		return ""
	}
	return dir
}

func (a *App) InstallService() AdminResult   { return a.runAdmin("install", "Service installed") }
func (a *App) UninstallService() AdminResult { return a.runAdmin("uninstall", "Service uninstalled") }
func (a *App) RestartService() AdminResult   { return a.runAdmin("restart", "Service restarted") }

// runAdmin is the shared body for the three service-control bindings.
// Emits footer events (work / err / ok) around the UAC subprocess; the
// AdminResult fields (OK / Cancelled / ErrorMessage) drive whatever
// post-action behavior the SPA needs (e.g., reloading device tables).
func (a *App) runAdmin(action, successMsg string) AdminResult {
	a.emitEvent("footer:set", map[string]string{"kind": "work", "text": "Working…"})
	errMsg, err := RunElevatedAdminAction(action)
	switch {
	case errors.Is(err, ErrUserCancelled):
		a.emitEvent("footer:set", map[string]string{"kind": "info", "text": "Cancelled."})
		return AdminResult{Cancelled: true}
	case err != nil:
		a.emitEvent("footer:set", map[string]interface{}{"kind": "err", "text": "Failed: " + err.Error()})
		return AdminResult{ErrorMessage: err.Error()}
	case errMsg != "":
		a.emitEvent("footer:set", map[string]interface{}{"kind": "err", "text": "Failed: " + errMsg})
		return AdminResult{ErrorMessage: errMsg}
	}
	a.emitEvent("footer:set", map[string]interface{}{
		"kind": "ok",
		"text": successMsg + " at " + time.Now().Format("15:04:05"),
	})
	return AdminResult{OK: true}
}

func (a *App) TriggerProbe(_ string) {} // Implemented in Task 16.

func (a *App) CheckForUpdate() { go runUpdateCheckEvent(a) }

func (a *App) DownloadUpdate() {
	go ctlDownloadEvent(a)
}

func (a *App) CancelDownload() {
	a.updateCh.mu.Lock()
	cancel := a.updateCh.dlCancel
	a.updateCh.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *App) InstallUpdate() AdminResult { return ctlInstallEvent(a) }

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

func (a *App) StartLogStream(id string) {
	if a.logTail == nil {
		a.logTail = &logTailController{}
	}
	a.logTail.start(id, a.emitEvent)
}

func (a *App) StopLogStream() {
	if a.logTail == nil {
		return
	}
	a.logTail.stop()
}

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

// --- Auto-update helpers ---

// applyUpdateEvent advances the update state machine and emits update:state.
func (a *App) applyUpdateEvent(ev UpdateEvent) {
	a.updateCh.mu.Lock()
	a.updateCh.state = nextUpdateState(a.updateCh.state, ev)
	st := a.updateCh.state
	tag := a.updateCh.release.TagName
	a.updateCh.mu.Unlock()
	a.emitEvent("update:state", map[string]interface{}{
		"state":       int(st),
		"release_tag": tag,
	})
}

// runUpdateCheckEvent fetches the latest release, compares against the current
// version, and emits the appropriate events. Body lifted from runUpdateCheck in
// panel.go, with apply() callbacks replaced by a.applyUpdateEvent().
func runUpdateCheckEvent(a *App) {
	exePath, err := os.Executable()
	if err != nil {
		writePanelDebugLog("update_check_exe_failed", err)
		return
	}
	installDir := filepath.Dir(exePath)

	updateUA := "SerialHop/" + version.Base() + " (auto-update; +https://github.com/bioexperiment-lab-devices/serialhop)"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rel, err := updater.LatestRelease(ctx, a.hc, updater.DefaultReleasesURL, updateUA)
	if err != nil {
		writePanelDebugLog("update_check_failed", err)
		return
	}
	newer, err := updater.IsNewer(rel.TagName, version.Version)
	if err != nil {
		writePanelDebugLog("update_check_parse_failed", err)
		return
	}
	if !newer {
		return
	}
	// Locate the asset for this Windows binary.
	var exeAsset *updater.Asset
	for i := range rel.Assets {
		name := rel.Assets[i].Name
		if strings.HasPrefix(name, "SerialHop-v") && strings.HasSuffix(name, ".exe") {
			exeAsset = &rel.Assets[i]
			break
		}
	}
	if exeAsset == nil {
		writePanelDebugLog("update_check_no_asset", fmt.Errorf("no SerialHop-v*.exe asset on release %s", rel.TagName))
		return
	}

	// Resume-from-disk: if a staged file under <installDir>/<assetName>
	// already exists, re-verify it against the current sums file. If it
	// matches, jump straight to UpdateReady.
	stagedPath := filepath.Join(installDir, exeAsset.Name)
	if _, err := os.Stat(stagedPath); err == nil {
		sumsAsset := rel.AssetByName("SHA256SUMS.txt")
		if sumsAsset != nil {
			body, ferr := fetchSums(a.hc, updateUA, sumsAsset.BrowserDownloadURL)
			if ferr == nil && updater.VerifyFile(stagedPath, body, exeAsset.Name) == nil {
				a.updateCh.mu.Lock()
				a.updateCh.release = rel
				a.updateCh.exeAsset = exeAsset
				a.updateCh.exeFile = stagedPath
				a.updateCh.mu.Unlock()
				a.applyUpdateEvent(EvUpdateAvailable)
				a.applyUpdateEvent(EvDownloadStart)
				a.applyUpdateEvent(EvDownloadOK)
				cleanupStaleStagedFiles(installDir, exeAsset.Name)
				return
			}
		}
		// Stale or unverifiable staged file: delete it.
		_ = os.Remove(stagedPath)
	}

	cleanupStaleStagedFiles(installDir, exeAsset.Name)

	a.updateCh.mu.Lock()
	a.updateCh.release = rel
	a.updateCh.exeAsset = exeAsset
	a.updateCh.mu.Unlock()
	a.applyUpdateEvent(EvUpdateAvailable)
}

// ctlDownloadEvent runs the download with progress emission. Lifted from
// panel.go's ctlDownload, with mw.Synchronize replaced by a.emitEvent.
func ctlDownloadEvent(a *App) {
	updateUA := "SerialHop/" + version.Base() + " (auto-update; +https://github.com/bioexperiment-lab-devices/serialhop)"

	exePath, err := os.Executable()
	if err != nil {
		writePanelDebugLog("update_download_exe_failed", err)
		return
	}
	installDir := filepath.Dir(exePath)

	a.updateCh.mu.Lock()
	rel := a.updateCh.release
	asset := a.updateCh.exeAsset
	a.updateCh.mu.Unlock()
	if asset == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	a.updateCh.mu.Lock()
	a.updateCh.dlCancel = cancel
	a.updateCh.mu.Unlock()
	defer func() {
		a.updateCh.mu.Lock()
		a.updateCh.dlCancel = nil
		a.updateCh.mu.Unlock()
		cancel()
	}()

	a.applyUpdateEvent(EvDownloadStart)

	dest := filepath.Join(installDir, asset.Name)
	var lastReport time.Time
	progress := func(received, total int64) {
		if time.Since(lastReport) < 200*time.Millisecond && (total <= 0 || received < total) {
			return
		}
		lastReport = time.Now()
		var msg string
		var pct float64
		if total > 0 {
			pct = float64(received) / float64(total) * 100
			msg = fmt.Sprintf("Downloading %.0f%% (%.1f / %.1f MB)", pct, float64(received)/1e6, float64(total)/1e6)
		} else {
			msg = fmt.Sprintf("Downloading %.1f MB", float64(received)/1e6)
		}
		a.emitEvent("footer:set", map[string]interface{}{"kind": "work", "text": msg, "progress": pct})
	}
	if err := updater.Download(ctx, a.hc, asset.BrowserDownloadURL, dest, updateUA, progress); err != nil {
		if errors.Is(err, context.Canceled) {
			a.emitEvent("footer:set", map[string]interface{}{"kind": "info", "text": "Download cancelled."})
			a.applyUpdateEvent(EvCancel)
			return
		}
		writePanelDebugLog("update_download_failed", err)
		a.applyUpdateEvent(EvDownloadFail)
		return
	}

	sumsAsset := rel.AssetByName("SHA256SUMS.txt")
	if sumsAsset == nil {
		_ = os.Remove(dest)
		writePanelDebugLog("update_no_sums_asset", fmt.Errorf("release %s has no SHA256SUMS.txt", rel.TagName))
		a.applyUpdateEvent(EvDownloadFail)
		return
	}
	body, err := fetchSums(a.hc, updateUA, sumsAsset.BrowserDownloadURL)
	if err != nil {
		_ = os.Remove(dest)
		writePanelDebugLog("update_fetch_sums_failed", err)
		a.applyUpdateEvent(EvDownloadFail)
		return
	}
	if err := updater.VerifyFile(dest, body, asset.Name); err != nil {
		_ = os.Remove(dest)
		writePanelDebugLog("update_verify_failed", err)
		a.applyUpdateEvent(EvDownloadFail)
		return
	}

	a.updateCh.mu.Lock()
	a.updateCh.exeFile = dest
	a.updateCh.mu.Unlock()

	a.emitEvent("footer:set", map[string]interface{}{"kind": "ok", "text": "Download complete."})
	a.applyUpdateEvent(EvDownloadOK)
}

// ctlInstallEvent runs the UAC-elevated install. Lifted from panel.go's
// ctlInstall. Returns AdminResult to the binding caller and emits events.
func ctlInstallEvent(a *App) AdminResult {
	a.updateCh.mu.Lock()
	src := a.updateCh.exeFile
	a.updateCh.mu.Unlock()
	if src == "" {
		return AdminResult{}
	}
	a.applyUpdateEvent(EvInstallStart)
	a.emitEvent("footer:set", map[string]interface{}{"kind": "work", "text": "Installing update…"})

	errMsg, err := RunElevatedAdminAction("update", "--update-src="+src)
	switch {
	case errors.Is(err, ErrUserCancelled):
		a.emitEvent("footer:set", map[string]interface{}{"kind": "info", "text": "Cancelled."})
		a.applyUpdateEvent(EvCancel)
		return AdminResult{Cancelled: true}
	case err != nil:
		a.emitEvent("footer:set", map[string]interface{}{"kind": "err", "text": "Failed: " + err.Error()})
		a.applyUpdateEvent(EvInstallFail)
		return AdminResult{ErrorMessage: err.Error()}
	case errMsg != "":
		a.emitEvent("footer:set", map[string]interface{}{"kind": "err", "text": "Failed: " + errMsg})
		a.applyUpdateEvent(EvInstallFail)
		return AdminResult{ErrorMessage: errMsg}
	}

	a.emitEvent("footer:set", map[string]interface{}{
		"kind": "ok",
		"text": "Update applied at " + time.Now().Format("15:04:05"),
	})
	a.applyUpdateEvent(EvInstallOK)
	return AdminResult{OK: true}
}
