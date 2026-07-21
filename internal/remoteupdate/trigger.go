package remoteupdate

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/updater"
	"github.com/bioexperiment-lab-devices/serialhop/internal/updateresult"
)

// Request selects the update source. Empty => GitHub latest.
type Request struct {
	Version string // optional; "vX.Y.Z" or "X.Y.Z"
	URL     string // optional; https custom mirror
	SHA256  string // required iff URL set (64 hex chars)
}

// Accepted is the trigger outcome. Noop=true means already at the target.
type Accepted struct {
	To     string
	Noop   bool
	Reason string
}

// BadRequestError => HTTP 400.
type BadRequestError struct{ Msg string }

func (e *BadRequestError) Error() string { return e.Msg }

// UpstreamError => HTTP 502 (GitHub release/tag lookup failed synchronously).
type UpstreamError struct{ Err error }

func (e *UpstreamError) Error() string { return "release lookup failed: " + e.Err.Error() }
func (e *UpstreamError) Unwrap() error { return e.Err }

const jobTimeout = 5 * time.Minute

var (
	semverRe    = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	assetNameRe = regexp.MustCompile(`^SerialHop-v(\d+\.\d+\.\d+)\.exe$`)
)

// plan is the fully-resolved work handed to the background job.
type plan struct {
	from, to   string // dotted X.Y.Z (no leading v)
	assetName  string // SerialHop-v<to>.exe
	stagedPath string
	assetURL   string // where to download the .exe from
	sumsBody   string // "<hex>  <assetName>" for VerifyFile
}

// Trigger validates+resolves the request, then (unless noop) launches the
// background download/verify/spawn job. Returns immediately.
func (m *Manager) Trigger(ctx context.Context, req Request) (Accepted, error) {
	if !m.Enabled() {
		return Accepted{}, ErrDisabled
	}
	if !m.tryAcquire() {
		return Accepted{}, ErrInProgress
	}
	pl, acc, err := m.resolve(ctx, req)
	if err != nil || acc.Noop {
		m.release()
		return acc, err
	}
	_ = updateresult.Write(m.cfg.ResultPath, updateresult.Result{
		State: updateresult.StateDownloading, From: pl.from, To: pl.to,
		StartedAt: nowRFC3339(),
	})
	slog.Info("remote_update triggered", "to", pl.to, "from", pl.from, "custom", req.URL != "")
	m.cfg.RunBackground(func() {
		defer m.release()
		m.runJob(pl)
	})
	return Accepted{To: pl.to}, nil
}

// resolve validates the request and, for GitHub modes, does the synchronous
// release lookup + noop check.
func (m *Manager) resolve(ctx context.Context, req Request) (plan, Accepted, error) {
	if req.URL != "" {
		return m.resolveCustom(req)
	}
	return m.resolveGitHub(ctx, req)
}

func (m *Manager) resolveCustom(req Request) (plan, Accepted, error) {
	if !strings.HasPrefix(req.URL, "https://") {
		return plan{}, Accepted{}, &BadRequestError{Msg: "url must be https://"}
	}
	if len(req.SHA256) != 64 || !isHex(req.SHA256) {
		return plan{}, Accepted{}, &BadRequestError{Msg: "sha256 must be 64 hex chars when url is set"}
	}
	ver, err := customVersion(req)
	if err != nil {
		return plan{}, Accepted{}, err
	}
	asset := "SerialHop-v" + ver + ".exe"
	return plan{
		from: m.cfg.CurVersion, to: ver, assetName: asset,
		stagedPath: filepath.Join(m.cfg.StagingDir, asset),
		assetURL:   req.URL,
		sumsBody:   strings.ToLower(req.SHA256) + "  " + asset,
	}, Accepted{}, nil
}

func (m *Manager) resolveGitHub(ctx context.Context, req Request) (plan, Accepted, error) {
	url := m.cfg.ReleasesURL
	if req.Version != "" {
		v, err := normalizeVersion(req.Version)
		if err != nil {
			return plan{}, Accepted{}, err
		}
		url = m.cfg.TagURL("v" + v)
	}
	lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rel, err := updater.LatestRelease(lctx, m.cfg.HTTPClient, url, m.cfg.UserAgent)
	if err != nil {
		return plan{}, Accepted{}, &UpstreamError{Err: err}
	}
	ver := strings.TrimPrefix(rel.TagName, "v")
	if !semverRe.MatchString(ver) {
		return plan{}, Accepted{}, &UpstreamError{Err: fmt.Errorf("release tag %q not X.Y.Z", rel.TagName)}
	}
	if cmp, err := updater.Compare(ver, m.cfg.CurVersion); err == nil && cmp == 0 {
		return plan{}, Accepted{To: ver, Noop: true, Reason: "already at " + ver}, nil
	}
	asset := "SerialHop-v" + ver + ".exe"
	a := rel.AssetByName(asset)
	sums := rel.AssetByName("SHA256SUMS.txt")
	if a == nil || sums == nil {
		return plan{}, Accepted{}, &UpstreamError{Err: fmt.Errorf("release %s missing %s or SHA256SUMS.txt", ver, asset)}
	}
	body, err := m.fetchText(ctx, sums.BrowserDownloadURL)
	if err != nil {
		return plan{}, Accepted{}, &UpstreamError{Err: fmt.Errorf("fetch sums: %w", err)}
	}
	return plan{
		from: m.cfg.CurVersion, to: ver, assetName: asset,
		stagedPath: filepath.Join(m.cfg.StagingDir, asset),
		assetURL:   a.BrowserDownloadURL, sumsBody: body,
	}, Accepted{}, nil
}

// runJob downloads, verifies, then spawns the detached swap child. Writes the
// result-file state at each transition. On download/verify failure it is the
// terminal writer (no child spawned).
func (m *Manager) runJob(pl plan) {
	ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
	defer cancel()

	lastPct := -1
	progress := func(recv, total int64) {
		if total <= 0 {
			return
		}
		pct := int(recv * 100 / total)
		if pct/5 == lastPct/5 {
			return
		}
		lastPct = pct
		_ = updateresult.Write(m.cfg.ResultPath, updateresult.Result{
			State: updateresult.StateDownloading, From: pl.from, To: pl.to, Pct: pct,
			StartedAt: m.startedAt(),
		})
	}

	if err := updater.Download(ctx, m.cfg.HTTPClient, pl.assetURL, pl.stagedPath, m.cfg.UserAgent, progress); err != nil {
		m.fail(pl, "download: "+err.Error())
		return
	}
	_ = updateresult.Write(m.cfg.ResultPath, updateresult.Result{
		State: updateresult.StateVerifying, From: pl.from, To: pl.to, StartedAt: m.startedAt(),
	})
	if err := updater.VerifyFile(pl.stagedPath, pl.sumsBody, pl.assetName); err != nil {
		_ = removeQuiet(pl.stagedPath)
		m.fail(pl, "verify: "+err.Error())
		return
	}
	_ = updateresult.Write(m.cfg.ResultPath, updateresult.Result{
		State: updateresult.StateInstalling, From: pl.from, To: pl.to, StartedAt: m.startedAt(),
	})
	args := []string{
		"--admin-action=update",
		"--update-src=" + pl.stagedPath,
		"--update-result=" + m.cfg.ResultPath,
		"--update-from=" + pl.from,
		"--update-to=" + pl.to,
	}
	if err := m.cfg.Spawn(m.cfg.ExePath, args); err != nil {
		m.fail(pl, "spawn: "+err.Error())
		return
	}
	slog.Info("remote_update spawned child", "to", pl.to)
}

func (m *Manager) fail(pl plan, msg string) {
	slog.Warn("remote_update failed", "to", pl.to, "err", msg)
	_ = updateresult.Write(m.cfg.ResultPath, updateresult.Result{
		State: updateresult.StateFailed, From: pl.from, To: pl.to, Error: msg,
		StartedAt: m.startedAt(), FinishedAt: nowRFC3339(),
	})
}

// startedAt preserves the original started_at across state writes.
func (m *Manager) startedAt() string {
	if r, err := updateresult.Read(m.cfg.ResultPath); err == nil && r.StartedAt != "" {
		return r.StartedAt
	}
	return nowRFC3339()
}

func (m *Manager) fetchText(ctx context.Context, url string) (string, error) {
	fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(fctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", m.cfg.UserAgent)
	resp, err := m.cfg.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return string(b), err
}

func customVersion(req Request) (string, error) {
	if req.Version != "" {
		return normalizeVersion(req.Version)
	}
	base := path.Base(req.URL)
	if mm := assetNameRe.FindStringSubmatch(base); mm != nil {
		return mm[1], nil
	}
	return "", &BadRequestError{Msg: "custom url needs a version: set \"version\" or name the file SerialHop-vX.Y.Z.exe"}
}

func normalizeVersion(s string) (string, error) {
	v := strings.TrimPrefix(s, "v")
	if !semverRe.MatchString(v) {
		return "", &BadRequestError{Msg: fmt.Sprintf("version %q must be X.Y.Z", s)}
	}
	return v, nil
}

func isHex(s string) bool {
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
			// valid hex digit
		default:
			return false
		}
	}
	return true
}

func removeQuiet(p string) error { return osRemove(p) }
