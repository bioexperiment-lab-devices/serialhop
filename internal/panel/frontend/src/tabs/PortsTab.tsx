import { useEffect, useState } from "react";
import { Button } from "../components/Button";
import { Help } from "../components/Help";
import { DiagnosticsDetails } from "../components/DiagnosticsDetails";
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

// JS-side timeout wrapper. Wails binding promises can stall forever if
// the bridge mis-delivers a response; that previously rendered as the
// stuck "Can't reach the local service" empty-state because the initial
// React state has reachable=false. With this wrapper, a stuck call
// surfaces as an actionable banner instead of looking like normal
// unreachability.
const BINDING_TIMEOUT_MS = 10_000;

function withTimeout<T>(p: Promise<T>, ms: number, label: string): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const t = setTimeout(() => reject(new Error(`${label} timed out after ${ms} ms`)), ms);
    p.then(v => { clearTimeout(t); resolve(v); }, e => { clearTimeout(t); reject(e); });
  });
}

export function PortsTab() {
  // `loaded` distinguishes initial state from "first call returned
  // saying unreachable". The original code rendered the same "Can't
  // reach" banner in both situations, which is exactly what made it
  // impossible to tell whether the binding had even been called.
  const [resp, setResp] = useState<PortsResult>({ ports: [], status: { reachable: false } });
  const [busy, setBusy] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [callError, setCallError] = useState<string | null>(null);

  const refresh = async () => {
    setBusy(true);
    setCallError(null);
    try {
      const r = await withTimeout(GetPorts(), BINDING_TIMEOUT_MS, "GetPorts");
      // Defense in depth: see DevicesTab for the same rationale. The Go
      // binding now guarantees non-nil `ports`.
      setResp({ ...r, ports: r.ports ?? [] });
    } catch (e) {
      setCallError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
      setLoaded(true);
    }
  };
  const rediscover = async () => {
    setBusy(true);
    try { await Discover(); await refresh(); } finally { setBusy(false); }
  };

  useEffect(() => { refresh(); }, []);

  const banner = callError
    ? `Binding error: ${callError}. Show diagnostics below for details.`
    : !loaded
        ? "Loading…"
        : !resp.status.reachable
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
        </>
      )}
    </>
  );
}
