import { useEffect, useState } from "react";
import { Button } from "../components/Button";
import { DiagnosticsDetails } from "../components/DiagnosticsDetails";
import { GetDevices, Discover, DisconnectAll } from "../wails/go/main/App";

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

export function DevicesTab() {
  const [resp, setResp] = useState<DevicesResult>({ devices: [], discovered_at: null, status: { reachable: false } });
  const [busy, setBusy] = useState(false);

  const refresh = async () => {
    setBusy(true);
    try { setResp(await GetDevices()); } finally { setBusy(false); }
  };

  const rediscover = async () => {
    setBusy(true);
    try { setResp(await Discover()); } finally { setBusy(false); }
  };

  const disconnect = async () => {
    setBusy(true);
    try { await DisconnectAll(); await refresh(); } finally { setBusy(false); }
  };

  useEffect(() => { refresh(); }, []);

  const empty = resp.devices.length === 0;
  const banner = pickBanner(resp.status, empty, "devices");

  return (
    <>
      <div className="shp-toolbar">
        <div className="shp-toolbar__banner">
          {resp.discovered_at ? <>Discovered at <code>{fmtTime(resp.discovered_at)}</code></> : <span>Never run</span>}
        </div>
        <div className="shp-btn-row">
          <Button onClick={rediscover} disabled={busy || !resp.status.reachable}>Rediscover</Button>
          <Button onClick={disconnect} disabled={busy || !resp.status.reachable || empty}>Disconnect all</Button>
          <Button variant="ghost" onClick={refresh} disabled={busy}>Refresh</Button>
        </div>
      </div>
      {!resp.status.reachable && banner ? (
        <div className="shp-empty">
          <div className="shp-empty__body">
            {banner}
            <DiagnosticsDetails />
          </div>
        </div>
      ) : (
        <>
          {banner && <div className="shp-toolbar__banner" style={{ marginBottom: 8 }}>{banner}</div>}
          <div className="shp-table-wrap">
            <table className="shp-table">
              <thead><tr><th>ID</th><th>Type</th><th>Port</th></tr></thead>
              <tbody>
                {[...resp.devices].sort((a, b) => a.id.localeCompare(b.id)).map(d => (
                  <tr key={d.id}><td>{d.id}</td><td>{d.type}</td><td>{d.port}</td></tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
    </>
  );
}

function fmtTime(iso: string): string {
  const d = new Date(iso);
  return d.toLocaleTimeString();
}

function pickBanner(status: { reachable: boolean; reason?: string }, empty: boolean, tab: "devices" | "ports"): string | null {
  if (!status.reachable && status.reason === "service_down") {
    return "Service is not running. Start it from the Status tab.";
  }
  if (!status.reachable) {
    return "Can't reach the local service. It may have just started — wait a few seconds and click Refresh.";
  }
  if (empty && tab === "devices") return "No devices yet. Click Rediscover to probe serial ports.";
  if (empty && tab === "ports") return "No serial ports detected on this machine.";
  return null;
}
