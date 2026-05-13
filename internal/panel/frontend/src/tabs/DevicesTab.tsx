import { useEffect, useState } from "react";
import { Button } from "../components/Button";
import { GetDevices, Discover, DisconnectAll } from "../wails/go/main/App";

interface DeviceDTO {
  id: string;
  type: string;
  type_code: number;
  port: string;
}

interface DevicesResponse {
  devices: DeviceDTO[];
  discovered_at: string | null;
}

type Status = { reachable: boolean; reason?: string };

export function DevicesTab() {
  const [resp, setResp] = useState<DevicesResponse>({ devices: [], discovered_at: null });
  const [status, setStatus] = useState<Status>({ reachable: false });
  const [busy, setBusy] = useState(false);

  const refresh = async () => {
    setBusy(true);
    try {
      const [r, s] = await GetDevices();
      setResp(r); setStatus(s);
    } finally { setBusy(false); }
  };

  const rediscover = async () => {
    setBusy(true);
    try {
      const [r, s] = await Discover();
      setResp(r); setStatus(s);
    } finally { setBusy(false); }
  };

  const disconnect = async () => {
    setBusy(true);
    try { await DisconnectAll(); await refresh(); } finally { setBusy(false); }
  };

  useEffect(() => { refresh(); }, []);

  const empty = resp.devices.length === 0;
  const banner = pickBanner(status, empty, "devices");

  return (
    <div className="devices-tab">
      <div className="banner-row">
        <span>{resp.discovered_at ? `Discovered at ${fmtTime(resp.discovered_at)}` : "Never run"}</span>
      </div>
      <div className="actions">
        <Button onClick={rediscover} disabled={busy || !status.reachable}>Rediscover</Button>
        <Button onClick={disconnect} disabled={busy || !status.reachable || empty}>Disconnect all</Button>
        <Button variant="ghost" onClick={refresh} disabled={busy}>Refresh</Button>
      </div>
      {banner && <div className="empty-banner">{banner}</div>}
      <table className="devices-table">
        <thead><tr><th>ID</th><th>Type</th><th>Port</th></tr></thead>
        <tbody>
          {[...resp.devices].sort((a, b) => a.id.localeCompare(b.id)).map(d => (
            <tr key={d.id}><td>{d.id}</td><td>{d.type}</td><td>{d.port}</td></tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function fmtTime(iso: string): string {
  const d = new Date(iso);
  return d.toLocaleTimeString();
}

function pickBanner(status: Status, empty: boolean, tab: "devices" | "ports"): string | null {
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
