//go:build windows

package panel

import (
	"context"
	"encoding/json"
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
	"github.com/bioexperiment-lab-devices/serialhop/internal/bootstrap"
	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
	"github.com/bioexperiment-lab-devices/serialhop/internal/paths"
	"github.com/bioexperiment-lab-devices/serialhop/internal/updater"
	"github.com/bioexperiment-lab-devices/serialhop/internal/version"
)

// --- DTOs declared just for the binding surface. ---

// ButtonStateDTO mirrors state.ComputeButtons for the SPA's Install /
// Uninstall / Restart button enablement.
type ButtonStateDTO struct {
	Install   bool `json:"install"`
	Uninstall bool `json:"uninstall"`
	Restart   bool `json:"restart"`
}

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

// DevicesResult is GetDevices/Discover's combined return — the response
// fields plus the panel-internal reachability status. Wails v2 only
// exposes the first return value of a multi-return Go function to JS;
// embedding both into a single struct keeps the contract explicit.
type DevicesResult struct {
	api.DevicesResponse
	Status ServiceTabStatusDTO `json:"status"`
}

type DisconnectResult struct {
	api.DisconnectResponse
	Status ServiceTabStatusDTO `json:"status"`
}

type PortsResult struct {
	api.DetailedPortsResponse
	Status ServiceTabStatusDTO `json:"status"`
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
	// Saving may have changed lab_bridge.host/user/pass — re-probe so the
	// Server / Tunnel lamps reflect the new credentials immediately
	// instead of waiting up to 30 s for the next tick.
	a.kickNetProbes()
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
//
// Lamp refresh: the network lamps are flipped to "Checking…" before the
// UAC subprocess (instant visual feedback that the user's click landed),
// and re-probed after it returns regardless of outcome — so a cancelled
// or failed action still recovers to the actual state instead of leaving
// the lamps gray.
func (a *App) runAdmin(action, successMsg string) AdminResult {
	a.emitEvent("footer:set", map[string]string{"kind": "work", "text": "Working…"})
	a.markNetProbesChecking()
	defer a.kickNetProbes()
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

func (a *App) TriggerProbe(which string) {
	switch which {
	case "server":
		a.lamps.setServer(netLamp{kind: lampChecking})
		a.emitServerLamp()
		trySend(a.serverTrigger)
	case "tunnel":
		a.lamps.setTunnel(netLamp{kind: lampChecking})
		a.emitTunnelLamp()
		trySend(a.tunnelTrigger)
	}
}

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

// toTabStatus translates ServiceCli's three-way reachability outcome
// into the TS-facing DTO consumed by the Devices and Ports tabs.
func toTabStatus(s ServiceCliStatus) ServiceTabStatusDTO {
	switch s {
	case StatusOK:
		return ServiceTabStatusDTO{Reachable: true}
	case StatusServiceDown:
		return ServiceTabStatusDTO{Reachable: false, Reason: "service_down"}
	}
	return ServiceTabStatusDTO{Reachable: false, Reason: "unreachable"}
}

// callCtx returns a context for one binding HTTP call. We do NOT take
// context.Context as a method parameter on bound methods: Wails v2's
// "auto-inject context as first arg" behavior does not fire for methods
// reached through embedding (main.App embeds *panel.App). The JS-side
// call passes no arguments, Wails sees a method that expects one, and
// the bridge rejects with:
//
//	"error parsing arguments: received 0 arguments to method
//	 'main.App.GetDevices', expected 1"
//
// — which is exactly what surfaced as "Can't reach the local service"
// in the Devices/Ports tabs (the JS binding promise rejected, the tab
// fell back to its initial reachable=false state, and the empty-state
// banner happens to read "Can't reach...") and stayed broken across
// three speculative cache-anchor fixes.
//
// Use a fresh background context with a 6s timeout (matches the
// ServiceCli HTTP client's own 5s timeout with a small headroom).
// Anchoring to a.ctx so the call cancels at panel shutdown rather
// than the underlying socket lingering past Wails OnShutdown.
func (a *App) callCtx() (context.Context, context.CancelFunc) {
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, 6*time.Second)
}

func (a *App) GetDevices() DevicesResult {
	ctx, cancel := a.callCtx()
	defer cancel()
	resp, st, _ := a.svc.GetDevices(ctx)
	return DevicesResult{
		DevicesResponse: normalizeDevicesResponse(resp),
		Status:          toTabStatus(st),
	}
}

func (a *App) Discover() DevicesResult {
	ctx, cancel := a.callCtx()
	defer cancel()
	resp, st, _ := a.svc.Discover(ctx)
	return DevicesResult{
		DevicesResponse: normalizeDevicesResponse(resp),
		Status:          toTabStatus(st),
	}
}

func (a *App) DisconnectAll() DisconnectResult {
	ctx, cancel := a.callCtx()
	defer cancel()
	resp, st, _ := a.svc.DisconnectAll(ctx)
	if st == StatusOK {
		a.emitEvent("footer:set", map[string]interface{}{
			"kind": "ok",
			"text": fmt.Sprintf("Disconnected %d device(s).", resp.Released),
		})
	}
	return DisconnectResult{DisconnectResponse: resp, Status: toTabStatus(st)}
}

func (a *App) GetPorts() PortsResult {
	ctx, cancel := a.callCtx()
	defer cancel()
	resp, st, _ := a.svc.GetPorts(ctx)
	return PortsResult{
		DetailedPortsResponse: normalizeDetailedPortsResponse(resp),
		Status:                toTabStatus(st),
	}
}

// DiagnosticsDTO is the support-bundle the panel exposes to the SPA
// when "Can't reach the local service" reports come in. Returning a
// structured snapshot is more useful than asking operators to grep
// log files: every field that gates reachability is in one place, in
// the SAME process that handles GetPorts/GetDevices.
type DiagnosticsDTO struct {
	PanelVersion            string `json:"panel_version"`
	CachePath               string `json:"cache_path"`
	CacheExists             bool   `json:"cache_exists"`
	CacheReadError          string `json:"cache_read_error,omitempty"`
	CacheUser               string `json:"cache_user,omitempty"`
	CacheFetchedAt          string `json:"cache_fetched_at,omitempty"`
	CacheActualRestPort     int    `json:"cache_actual_rest_port"`
	BaseURLResolved         string `json:"base_url_resolved,omitempty"`
	BaseURLStatus           string `json:"base_url_status"`
	ConfiguredLabBridgeUser string `json:"configured_lab_bridge_user"`
	ConfigPath              string `json:"config_path"`
	DataDir                 string `json:"data_dir"`
	PanelErrorLogPath       string `json:"panel_error_log_path"`

	// HTTP-probe result — the panel actually makes a GET /serial/ports/detailed
	// against the resolved local port using the same ServiceCli path the
	// Devices/Ports tabs use. Reports per-phase outcome so we can tell
	// "cache OK but loopback dead" from "cache OK and HTTP works but the
	// JS layer is dropping the result." This is the part `BaseURLStatus`
	// alone can't reveal — `BaseURLStatus` only checks the cache.
	HTTPProbeStatus   string `json:"http_probe_status"`          // "ok" | "service_down" | "unreachable" | "skipped"
	HTTPProbeError    string `json:"http_probe_error,omitempty"` // raw error from Do/decode if any
	HTTPProbeDurMs    int64  `json:"http_probe_duration_ms"`     // wall-clock duration
	HTTPProbePortsLen int    `json:"http_probe_ports_len"`       // number of ports in the response on success
}

// Diagnostics returns a snapshot of every input that gates the
// Devices/Ports reachability check. Bound for the SPA so support
// requests can paste a single JSON blob instead of hunting for log
// files under %ProgramData%\SerialHop\logs\.
func (a *App) Diagnostics() DiagnosticsDTO {
	cfg, _ := config.LoadPartial(paths.ConfigPath())
	d := DiagnosticsDTO{
		PanelVersion:            version.Base(),
		CachePath:               paths.ServerInfoCachePath(),
		ConfiguredLabBridgeUser: cfg.LabBridge.User,
		ConfigPath:              paths.ConfigPath(),
		DataDir:                 paths.DataDir(),
		PanelErrorLogPath:       paths.PanelErrorLogPath(),
	}
	if _, err := os.Stat(d.CachePath); err == nil {
		d.CacheExists = true
	}
	// Read the cache file directly so Diagnostics can report the
	// cache_user as written by the SERVICE, independent of whether it
	// matches the panel's currently-configured lab_bridge.user — that
	// mismatch is itself useful debug info, and ReadCache would
	// collapse it into ErrCacheMissing.
	if d.CachePath != "" {
		if data, err := os.ReadFile(d.CachePath); err != nil {
			if !os.IsNotExist(err) {
				d.CacheReadError = err.Error()
			}
		} else {
			var c bootstrap.Cache
			if jerr := json.Unmarshal(data, &c); jerr != nil {
				d.CacheReadError = "parse: " + jerr.Error()
			} else {
				d.CacheUser = c.User
				d.CacheFetchedAt = c.FetchedAt
				d.CacheActualRestPort = c.ActualRestPort
			}
		}
	}
	if a.svc != nil {
		base, st := a.svc.baseURL()
		d.BaseURLResolved = base
		switch st {
		case StatusOK:
			d.BaseURLStatus = "ok"
		case StatusServiceDown:
			d.BaseURLStatus = "service_down"
		default:
			d.BaseURLStatus = "unreachable"
		}
	} else {
		d.BaseURLStatus = "no_servicecli"
	}
	// HTTP probe: directly call /serial/ports/detailed at the resolved
	// loopback URL so the diagnostics snapshot captures the actual
	// transport-layer error (which ServiceCli.do swallows when it maps
	// every transport failure to StatusServiceDown). This is the piece
	// `BaseURLStatus` can't reveal — `BaseURLStatus` only checks the
	// cache. If the cache resolves a port but the panel still renders
	// "Can't reach", the HTTP error here will say why (refused, timeout,
	// IPv6 vs IPv4 mismatch, AV/firewall blocking loopback, ...).
	if d.BaseURLResolved == "" {
		d.HTTPProbeStatus = "skipped"
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, "GET", d.BaseURLResolved+"/serial/ports/detailed", nil)
		hc := &http.Client{Timeout: 6 * time.Second}
		t0 := time.Now()
		resp, err := hc.Do(req)
		d.HTTPProbeDurMs = time.Since(t0).Milliseconds()
		switch {
		case err != nil:
			d.HTTPProbeStatus = "service_down"
			d.HTTPProbeError = err.Error()
		case resp.StatusCode != 200:
			d.HTTPProbeStatus = "service_down"
			d.HTTPProbeError = fmt.Sprintf("status=%d", resp.StatusCode)
			_ = resp.Body.Close()
		default:
			var body api.DetailedPortsResponse
			if derr := json.NewDecoder(resp.Body).Decode(&body); derr != nil {
				d.HTTPProbeStatus = "service_down"
				d.HTTPProbeError = "decode: " + derr.Error()
			} else {
				d.HTTPProbeStatus = "ok"
				d.HTTPProbePortsLen = len(body.Ports)
			}
			_ = resp.Body.Close()
		}
	}
	return d
}

// StartLogStream attaches the panel's log tailer to the given stream and
// returns the recent backlog (oldest first). The SPA prepends these lines
// to its in-memory ring before subscribing to subsequent log:line events
// — backlog comes back via the return value rather than as events to
// avoid a race where the events fire before the SPA's EventsOn handler
// is registered.
func (a *App) StartLogStream(id string) []map[string]interface{} {
	if a.logTail == nil {
		a.logTail = &logTailController{}
	}
	return a.logTail.start(id, a.emitEvent)
}

func (a *App) StopLogStream() {
	if a.logTail == nil {
		return
	}
	a.logTail.stop()
}

// RecordFrontendCrash appends one JSON line to the panel crash journal.
// Called by the React ErrorBoundary fallback and the JS-side global
// `error` / `unhandledrejection` listeners.
//
// String-only parameters by design. The method MUST NOT take
// context.Context — methods on *panel.App reached via main.App
// embedding don't get auto-injection (see
// TestBindings_NoMethodTakesContextContext for the regression gate).
// The journal write is fully synchronous and best-effort; failures are
// swallowed inside appendCrashJournal so the safety net itself can never
// throw inside a crash-recording path.
func (a *App) RecordFrontendCrash(message, source, stack string) {
	appendCrashJournal(message, source, stack, version.Base(), time.Now().UTC())
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
	a.markNetProbesChecking()
	defer a.kickNetProbes()

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
