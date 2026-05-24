import { useEffect, useState, useCallback } from "react";
import { Button } from "../components/Button";
import { ListCameras, SetCameraArmed, RefreshCameras, type CameraView, type StreamingState } from "../wails/go/main/App";
import { useWailsEvent } from "../wailsEvents";

export function CamerasTab() {
  const [state, setState] = useState<StreamingState>({ cameras: [], ffmpeg_ok: true });
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setError(null);
    try {
      const r = await ListCameras();
      setState({ cameras: r.cameras ?? [], ffmpeg_ok: !!r.ffmpeg_ok });
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

      {state.cameras.length === 0 ? (
        <div className="shp-empty">
          No cameras detected. Connect a camera or check whether another application is using it.
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
