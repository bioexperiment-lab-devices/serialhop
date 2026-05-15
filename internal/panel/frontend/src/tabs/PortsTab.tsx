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

type EmptyKind = "service-down" | "no-ports" | "unreachable" | "binding-error";

export function PortsTab() {
  // `loaded` distinguishes initial state from "first call returned
  // saying unreachable". The original code rendered the same "Can't
  // reach" banner in both situations, which is exactly what made it
  // impossible to tell whether the binding had even been called.
  const [resp, setResp] = useState<PortsResult>({ ports: [], status: { reachable: false } });
  const [busy, setBusy] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [callError, setCallError] = useState<string | null>(null);
  const [selected, setSelected] = useState<string | null>(null);

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

  const total = resp.ports.length;
  const matched = resp.ports.filter(p => p.discovered).length;

  let banner: React.ReactNode = null;
  if (callError) {
    banner = `Binding error: ${callError}`;
  } else if (!loaded) {
    banner = "Loading…";
  } else if (!resp.status.reachable && resp.status.reason === "service_down") {
    banner = "Service is not running. Start it from the Status tab.";
  } else if (!resp.status.reachable) {
    banner = "Can't reach the local service. It may have just started — wait a few seconds and click Refresh.";
  } else if (total === 0) {
    banner = "No serial ports detected on this machine.";
  } else {
    banner = <><b>{total}</b> ports enumerated by the OS · <b>{matched}</b> matched to a device</>;
  }

  const emptyKind: EmptyKind | null = callError
    ? "binding-error"
    : !resp.status.reachable && resp.status.reason === "service_down"
      ? "service-down"
      : !resp.status.reachable
        ? "unreachable"
        : loaded && total === 0
          ? "no-ports"
          : null;

  return (
    <>
      <div className="shp-toolbar">
        <div className="shp-toolbar__banner">{banner}</div>
        <div className="shp-btn-row">
          <Button variant="primary" onClick={rediscover} disabled={busy || !resp.status.reachable}>↻ Rediscover</Button>
          <Button onClick={refresh} disabled={busy}>Refresh</Button>
        </div>
      </div>
      {emptyKind ? (
        <PortsEmpty kind={emptyKind} />
      ) : (
        <div className="shp-table-wrap">
          <table className="shp-table">
            <thead>
              <tr>
                <th>Name</th>
                <th>USB</th>
                <th>VID <Help title="VID" what="USB vendor ID in hexadecimal." /></th>
                <th>PID <Help title="PID" what="USB product ID in hexadecimal." /></th>
                <th>Serial № <Help title="Serial number" what="USB serial string if the device reports one." /></th>
                <th>Product <Help title="Product" what="USB product descriptor string." /></th>
                <th>Discovered <Help title="Discovered" what="True if discovery matched a SerialHop device on this port." /></th>
                <th>Device ID <Help title="Device ID" what="The logical device ID this port was bound to during the last discovery." /></th>
              </tr>
            </thead>
            <tbody>
              {[...resp.ports].sort((a, b) => a.name.localeCompare(b.name)).map(p => (
                <tr
                  key={p.name}
                  data-selected={selected === p.name ? true : undefined}
                  onClick={() => setSelected(prev => (prev === p.name ? null : p.name))}
                  style={{ cursor: "pointer" }}
                >
                  <td><b style={{ color: "var(--text)" }}>{p.name}</b></td>
                  <td>{p.is_usb ? <span className="shp-check">✓</span> : <span className="shp-dim">—</span>}</td>
                  <td>{p.vid || <span className="shp-dim">—</span>}</td>
                  <td>{p.pid || <span className="shp-dim">—</span>}</td>
                  <td>{p.serial_number || <span className="shp-dim">—</span>}</td>
                  <td style={{ whiteSpace: "normal", fontFamily: "'IBM Plex Sans', system-ui, sans-serif" }}>
                    {p.product || <span className="shp-dim">—</span>}
                  </td>
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

const EMPTY_COPY: Record<EmptyKind, { icon: string; title: string; body: string }> = {
  "service-down": {
    icon: "○ ○ ○",
    title: "Service is not running.",
    body: "Start the SerialHop service from the Status tab. Ports will appear once discovery has completed.",
  },
  "no-ports": {
    icon: "—",
    title: "No serial ports detected on this machine.",
    body: "Plug in a USB-to-serial adapter or development board and click Refresh.",
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

function PortsEmpty({ kind }: { kind: EmptyKind }) {
  const m = EMPTY_COPY[kind];
  return (
    <div className="shp-empty">
      <div className="shp-empty__icon">{m.icon}</div>
      <div className="shp-empty__title">{m.title}</div>
      <div className="shp-empty__body">{m.body}</div>
    </div>
  );
}
