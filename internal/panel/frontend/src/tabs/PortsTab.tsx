import { useEffect, useState } from "react";
import { Button } from "../components/Button";
import { Help } from "../components/Help";
import { GetPorts, Discover } from "../wails/go/main/App";

interface DetailedPortDTO {
  name: string;
  is_usb: boolean;
  vid: string;
  pid: string;
  serial_number: string;
  product: string;
  discovered: boolean;
  device_id?: string;
}

interface PortsResult {
  ports: DetailedPortDTO[];
  status: { reachable: boolean; reason?: string };
}

export function PortsTab() {
  const [resp, setResp] = useState<PortsResult>({ ports: [], status: { reachable: false } });
  const [busy, setBusy] = useState(false);

  const refresh = async () => {
    setBusy(true);
    try { setResp(await GetPorts()); } finally { setBusy(false); }
  };
  const rediscover = async () => {
    setBusy(true);
    try { await Discover(); await refresh(); } finally { setBusy(false); }
  };

  useEffect(() => { refresh(); }, []);

  const banner = !resp.status.reachable
    ? (resp.status.reason === "service_down"
        ? "Service is not running. Start it from the Status tab."
        : "Can't reach the local service. It may have just started — wait a few seconds and click Refresh.")
    : resp.ports.length === 0 ? "No serial ports detected on this machine." : null;

  return (
    <>
      <div className="shp-btn-row" style={{ marginBottom: 12 }}>
        <Button variant="ghost" onClick={refresh} disabled={busy}>Refresh</Button>
        <Button onClick={rediscover} disabled={busy || !resp.status.reachable}>Rediscover</Button>
      </div>
      {banner && (
        <div className="shp-empty">
          <div className="shp-empty__body">{banner}</div>
        </div>
      )}
      {!banner && (
        <div className="shp-table-wrap">
          <table className="shp-table">
            <thead>
              <tr>
                <th>Name</th>
                <th>USB</th>
                <th>VID <Help title="VID" what="USB vendor ID in hexadecimal." /></th>
                <th>PID <Help title="PID" what="USB product ID in hexadecimal." /></th>
                <th>Serial <Help title="Serial number" what="USB serial string if the device reports one." /></th>
                <th>Product <Help title="Product" what="USB product descriptor string." /></th>
                <th>Discovered <Help title="Discovered" what="True if discovery matched a SerialHop device on this port." /></th>
                <th>Device ID <Help title="Device ID" what="The logical device ID this port was bound to during the last discovery." /></th>
              </tr>
            </thead>
            <tbody>
              {[...resp.ports].sort((a, b) => a.name.localeCompare(b.name)).map(p => (
                <tr key={p.name}>
                  <td>{p.name}</td>
                  <td>{p.is_usb ? <span className="shp-check">✓</span> : <span className="shp-dim">—</span>}</td>
                  <td>{p.vid}</td>
                  <td>{p.pid}</td>
                  <td>{p.serial_number}</td>
                  <td>{p.product}</td>
                  <td>{p.discovered ? <span className="shp-check">✓</span> : <span className="shp-dim">—</span>}</td>
                  <td>{p.device_id || <span className="shp-dim">—</span>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}
