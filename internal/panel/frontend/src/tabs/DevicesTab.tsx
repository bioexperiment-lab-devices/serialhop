import { useEffect, useState } from "react";
import { Button } from "../components/Button";
import { GetDevices, Discover, DisconnectAll, DisconnectPort } from "../wails/go/main/App";

interface DeviceDTO {
  id: string;
  type: string;
  type_code: number;
  port: string;
}

interface DevicesResult {
  devices: DeviceDTO[];
  discovered_at: string | null;
  status: { reachable: boolean; reason?: string };
}

// Mirror of PortsTab's wrapper. See PortsTab.tsx for rationale.
const BINDING_TIMEOUT_MS = 10_000;

function withTimeout<T>(p: Promise<T>, ms: number, label: string): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const t = setTimeout(() => reject(new Error(`${label} timed out after ${ms} ms`)), ms);
    p.then(v => { clearTimeout(t); resolve(v); }, e => { clearTimeout(t); reject(e); });
  });
}

type EmptyKind = "service-down" | "never-discovered" | "unreachable" | "binding-error";

export function DevicesTab() {
  const [resp, setResp] = useState<DevicesResult>({ devices: [], discovered_at: null, status: { reachable: false } });
  const [busy, setBusy] = useState(false);
  const [discovering, setDiscovering] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [callError, setCallError] = useState<string | null>(null);

  const refresh = async () => {
    setBusy(true);
    setCallError(null);
    try {
      const r = await withTimeout(GetDevices(), BINDING_TIMEOUT_MS, "GetDevices");
      // Defense in depth: the Go binding now guarantees `devices` is a
      // non-nil array (see internal/panel/bindings_helpers.go), but if a
      // stale build or future refactor regresses to `null`, normalize on
      // the way into state so the render path can't throw on
      // `null.length` and blank the window.
      setResp({ ...r, devices: r.devices ?? [] });
    } catch (e) {
      setCallError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
      setLoaded(true);
    }
  };

  const rediscover = async () => {
    setBusy(true);
    setDiscovering(true);
    setCallError(null);
    try {
      const r = await withTimeout(Discover(), BINDING_TIMEOUT_MS, "Discover");
      setResp({ ...r, devices: r.devices ?? [] });
    } catch (e) {
      setCallError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
      setDiscovering(false);
      setLoaded(true);
    }
  };

  const disconnect = async () => {
    setBusy(true);
    try { await DisconnectAll(); await refresh(); } finally { setBusy(false); }
  };

  const disconnectOne = async (port: string) => {
    setBusy(true);
    try { await DisconnectPort(port); await refresh(); } finally { setBusy(false); }
  };

  useEffect(() => { refresh(); }, []);

  const empty = resp.devices.length === 0;
  const count = resp.devices.length;

  let banner: React.ReactNode = null;
  if (callError) {
    banner = `Binding error: ${callError}`;
  } else if (!loaded) {
    banner = "Loading…";
  } else if (discovering) {
    banner = <><b>Probing serial ports…</b> closing existing connections.</>;
  } else if (!resp.status.reachable && resp.status.reason === "service_down") {
    banner = "Service is not running. Start it from the Status tab.";
  } else if (!resp.status.reachable) {
    banner = "Can't reach the local service. It may have just started — wait a few seconds and click Refresh.";
  } else if (empty) {
    banner = <>No devices yet. Click <b>Rediscover</b> to probe serial ports.</>;
  } else if (resp.discovered_at) {
    banner = <>Discovered at <code>{fmtTime(resp.discovered_at)}</code> · <b>{count}</b> {count === 1 ? "device" : "devices"}</>;
  } else {
    banner = <span>Never run</span>;
  }

  const emptyKind: EmptyKind | null = callError
    ? "binding-error"
    : !resp.status.reachable && resp.status.reason === "service_down"
      ? "service-down"
      : !resp.status.reachable
        ? "unreachable"
        : empty && loaded && !discovering
          ? "never-discovered"
          : null;

  const showEmpty = emptyKind !== null && !discovering;

  return (
    <>
      <div className="shp-toolbar">
        <div className="shp-toolbar__banner">{banner}</div>
        <div className="shp-btn-row">
          <Button variant="primary" onClick={rediscover} disabled={busy || !resp.status.reachable}>↻ Rediscover</Button>
          <Button variant="danger" onClick={disconnect} disabled={busy || !resp.status.reachable || empty}>Disconnect all</Button>
          <Button onClick={refresh} disabled={busy}>Refresh</Button>
        </div>
      </div>
      {showEmpty ? (
        <DevicesEmpty kind={emptyKind!} />
      ) : (
        <div
          className="shp-table-wrap"
          style={discovering ? { opacity: 0.5, transition: "opacity 0.2s" } : undefined}
        >
          <table className="shp-table">
            <thead>
              <tr>
                <th style={{ width: "30%" }}>ID</th>
                <th style={{ width: "30%" }}>Type</th>
                <th style={{ width: "30%" }}>Port</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {[...resp.devices].sort((a, b) => a.id.localeCompare(b.id)).map(d => (
                <tr key={d.id}>
                  <td><b style={{ color: "var(--text)" }}>{d.id}</b></td>
                  <td>{d.type}</td>
                  <td>{d.port}</td>
                  <td style={{ textAlign: "right" }}>
                    <Button
                      variant="danger"
                      onClick={() => disconnectOne(d.port)}
                      disabled={busy || !resp.status.reachable}
                      aria-label={`Disconnect ${d.id}`}
                    >
                      Disconnect
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}

function fmtTime(iso: string): string {
  const d = new Date(iso);
  return d.toLocaleTimeString();
}

const EMPTY_COPY: Record<EmptyKind, { icon: string; title: string; body: string }> = {
  "service-down": {
    icon: "○ ○ ○",
    title: "Service is not running.",
    body: "Start the SerialHop service from the Status tab. Devices will appear here once it's running and discovery has completed.",
  },
  "never-discovered": {
    icon: "—",
    title: "No devices have been discovered yet.",
    body: "Click Rediscover to probe the available serial ports. This may take several seconds; existing connections will be closed.",
  },
  "unreachable": {
    icon: "?",
    title: "Can't reach the local service.",
    body: "It may have just started. Wait a few seconds and click Refresh. If the problem persists, check the service from the Status tab.",
  },
  "binding-error": {
    icon: "!",
    title: "Couldn't talk to the panel backend.",
    body: "A binding call failed. Use Refresh to retry. If it keeps happening, restart the panel from the system tray.",
  },
};

function DevicesEmpty({ kind }: { kind: EmptyKind }) {
  const m = EMPTY_COPY[kind];
  return (
    <div className="shp-empty">
      <div className="shp-empty__icon">{m.icon}</div>
      <div className="shp-empty__title">{m.title}</div>
      <div className="shp-empty__body">{m.body}</div>
    </div>
  );
}
