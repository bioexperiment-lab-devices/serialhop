import { useEffect, useState, useCallback } from "react";
import { Button } from "../components/Button";
import {
  ListCameras,
  SetCameraArmed,
  RefreshCameras,
  DiagnoseCameras,
  type CameraView,
  type StreamingState,
  type FFmpegDiagnostics,
} from "../wails/go/main/App";
import { useWailsEvent } from "../wailsEvents";

export function CamerasTab() {
  const [state, setState] = useState<StreamingState>({ cameras: [], ffmpeg_ok: true });
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [diagnostics, setDiagnostics] = useState<FFmpegDiagnostics | null>(null);
  const [diagnosing, setDiagnosing] = useState(false);

  const load = useCallback(async () => {
    setError(null);
    try {
      const r = await ListCameras();
      setState({
        cameras: r.cameras ?? [],
        ffmpeg_ok: !!r.ffmpeg_ok,
        last_enum_error: r.last_enum_error,
      });
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { load(); }, [load]);
  useEffect(() => useWailsEvent("streaming:state", () => { load(); }), [load]);

  const refresh = async () => {
    setRefreshing(true);
    try { await RefreshCameras(); await load(); } finally { setRefreshing(false); }
  };

  const diagnose = async () => {
    setDiagnosing(true);
    setDiagnostics(null);
    try {
      setDiagnostics(await DiagnoseCameras());
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setDiagnosing(false);
    }
  };

  const setArmed = async (id: string, armed: boolean) => {
    // Optimistic update
    setState(s => ({
      ...s,
      cameras: s.cameras.map(c => c.id === id ? { ...c, armed } : c),
    }));
    try {
      await SetCameraArmed(id, armed);
    } catch (e) {
      // Revert on failure.
      setState(s => ({
        ...s,
        cameras: s.cameras.map(c => c.id === id ? { ...c, armed: !armed } : c),
      }));
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  if (loading) return <div className="shp-pad">Loading cameras…</div>;

  const armedCount = state.cameras.filter(c => c.armed).length;

  return (
    <div className="shp-pad">
      <div className="shp-row shp-row--header">
        <h2>Cameras</h2>
        <div className="shp-spacer" />
        <span className="shp-meta">{armedCount}/{state.cameras.length} armed</span>
        <Button onClick={refresh} disabled={refreshing}>{refreshing ? "Refreshing…" : "Refresh"}</Button>
        <Button onClick={diagnose} disabled={diagnosing}>{diagnosing ? "Diagnosing…" : "Diagnose"}</Button>
      </div>

      {!state.ffmpeg_ok && (
        <div className="shp-banner shp-banner--error" role="alert">
          ffmpeg.exe missing or modified — reinstall SerialHop to enable camera streaming.
        </div>
      )}

      {error && (
        <div className="shp-banner shp-banner--warning" role="alert">
          {error}
          <Button onClick={() => setError(null)}>Dismiss</Button>
        </div>
      )}

      {state.last_enum_error && (
        <div className="shp-banner shp-banner--warning" role="alert">
          Enumeration error: {state.last_enum_error}
        </div>
      )}

      {diagnostics && <DiagnosticsPanel d={diagnostics} onDismiss={() => setDiagnostics(null)} />}

      {state.cameras.length === 0 ? (
        <div className="shp-empty">
          No cameras detected. Possible causes:
          <ul>
            <li>The camera is in use by another application.</li>
            <li>The camera is exposed via MediaFoundation only (newer integrated webcams). DirectShow can't see it.</li>
            <li>The bundled ffmpeg.exe wasn't installed correctly — click <b>Diagnose</b> to check.</li>
          </ul>
        </div>
      ) : (
        <div className="shp-cards">
          {state.cameras.map(c => (
            <CameraCard key={c.id} camera={c} onToggle={a => setArmed(c.id, a)} />
          ))}
        </div>
      )}
    </div>
  );
}

interface CameraCardProps {
  camera: CameraView;
  onToggle: (next: boolean) => void;
}

function CameraCard({ camera, onToggle }: CameraCardProps) {
  const badge = badgeFor(camera);
  return (
    <div className="shp-card">
      <div className="shp-row">
        <div>
          <div className="shp-card__title">{camera.label}</div>
          <div className="shp-card__id" title={camera.id}>{camera.id}</div>
        </div>
        <div className="shp-spacer" />
        <span className={`shp-badge shp-badge--${badge.kind}`}>{badge.label}</span>
      </div>
      <div className="shp-row">
        <label className="shp-switch">
          <input
            type="checkbox"
            role="switch"
            aria-label={`Allow streaming ${camera.label}`}
            checked={camera.armed}
            onChange={e => onToggle(e.target.checked)}
          />
          <span>Allow streaming</span>
        </label>
      </div>
      {camera.last_error_msg && (
        <div className="shp-card__error">{camera.last_error_msg}</div>
      )}
    </div>
  );
}

function badgeFor(c: CameraView): { kind: string; label: string } {
  if (c.live) return { kind: "ok", label: "Live" };
  if (!c.connected) return { kind: "warn", label: "Disconnected" };
  if (c.armed) return { kind: "idle", label: "Armed" };
  return { kind: "muted", label: "Disarmed" };
}

interface DiagnosticsPanelProps {
  d: FFmpegDiagnostics;
  onDismiss: () => void;
}

function DiagnosticsPanel({ d, onDismiss }: DiagnosticsPanelProps) {
  return (
    <div className="shp-card" role="region" aria-label="Camera diagnostics">
      <div className="shp-row">
        <b>ffmpeg diagnostics</b>
        <div className="shp-spacer" />
        <Button onClick={onDismiss}>Dismiss</Button>
      </div>
      <div className="shp-card__id">Path: {d.ffmpeg_path || "(empty)"}</div>
      <div className="shp-card__id">Binary present: {d.binary_exists ? "yes" : "no"}</div>
      {d.version_line && <div className="shp-card__id">Version: {d.version_line}</div>}
      {d.version_error && (
        <div className="shp-card__error">Version probe failed: {d.version_error}</div>
      )}
      {d.list_devices_error && (
        <div className="shp-card__error">list_devices: {d.list_devices_error}</div>
      )}
      {d.list_devices_raw && (
        <>
          <div style={{ marginTop: "0.5rem" }}>
            <b>Raw output of <code>ffmpeg -list_devices true -f dshow -i dummy</code>:</b>
          </div>
          <pre style={{
            whiteSpace: "pre-wrap",
            wordBreak: "break-word",
            background: "rgba(0,0,0,0.05)",
            padding: "0.5rem",
            fontSize: "0.85em",
            maxHeight: "20rem",
            overflow: "auto",
          }}>{d.list_devices_raw}</pre>
        </>
      )}
    </div>
  );
}
