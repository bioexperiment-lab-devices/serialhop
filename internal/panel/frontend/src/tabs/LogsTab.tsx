import { useEffect, useRef, useState } from "react";
import { Button } from "../components/Button";
import { Help } from "../components/Help";
import { StartLogStream, StopLogStream, OpenLogsFolder } from "../wails/go/main/App";
import { EventsOn, EventsOff } from "../wails/runtime/runtime";
import type { LogLinePayload } from "../types";

type StreamID = "service" | "stderr" | "panel";
type LevelFilter = "all" | "debug" | "info" | "warn" | "error";

const RING_CAPACITY = 5_000;
const LEVEL_RANK: Record<string, number> = { debug: 0, info: 1, warn: 2, error: 3 };

// v1 simplification: spec §9 requires per-entry help for the Stream dropdown.
// Since native <select> options can't host inline icons, we render a single Help
// icon whose content changes based on the currently selected stream — same
// information density as per-option help, just surfaced after selection.
const streamHelp: Record<StreamID, { title: string; what: string }> = {
  service: {
    title: "Service log",
    what: "Structured JSON log from the SerialHop service (slog records). Time / Level / Message columns with full field detail on row click.",
  },
  stderr: {
    title: "Stderr",
    what: "Raw stderr output from the service process. Append-only.",
  },
  panel: {
    title: "Panel errors",
    what: "Errors written by the panel itself during startup or auto-update. No rotation.",
  },
};

export function LogsTab() {
  const [stream, setStream] = useState<StreamID>("service");
  const [level, setLevel] = useState<LevelFilter>("all");
  const [follow, setFollow] = useState(true);
  const [search, setSearch] = useState("");
  const [lines, setLines] = useState<LogLinePayload[]>([]);
  const [selected, setSelected] = useState<LogLinePayload | null>(null);
  const endRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    setLines([]); setSelected(null);
    StartLogStream(stream);
    const onLine = (p: LogLinePayload) => {
      if (p.stream !== stream) return;
      setLines(prev => {
        const next = [...prev, p];
        if (next.length > RING_CAPACITY) next.splice(0, next.length - RING_CAPACITY);
        return next;
      });
    };
    const onRot = () => setLines(prev => [...prev, { stream, raw: "— rotated —" }]);
    EventsOn("log:line", onLine);
    EventsOn("log:rotated", onRot);
    return () => { EventsOff("log:line"); EventsOff("log:rotated"); StopLogStream(); };
  }, [stream]);

  useEffect(() => { if (follow) endRef.current?.scrollIntoView({ behavior: "auto" }); }, [lines, follow]);

  const filtered = lines.filter(l => {
    if (stream === "service" && level !== "all" && l.record) {
      const recLevel = String(l.record.level || "").toLowerCase();
      if (LEVEL_RANK[recLevel] !== undefined && LEVEL_RANK[recLevel] < LEVEL_RANK[level]) return false;
    }
    if (search) {
      const hay = l.raw || JSON.stringify(l.record || {});
      if (!hay.toLowerCase().includes(search.toLowerCase())) return false;
    }
    return true;
  });

  return (
    <>
      <div className="shp-logs-controls">
        <label className="shp-row">
          <span style={{ marginRight: 4 }}>Stream:</span>
          <select className="shp-select" value={stream} onChange={e => setStream(e.target.value as StreamID)}>
            <option value="service">Service log</option>
            <option value="stderr">Stderr</option>
            <option value="panel">Panel errors</option>
          </select>
          <Help title={streamHelp[stream].title} what={streamHelp[stream].what} />
        </label>
        <label className="shp-row">
          <span style={{ marginRight: 4 }}>Level:</span>
          <select className="shp-select" value={level} onChange={e => setLevel(e.target.value as LevelFilter)} disabled={stream !== "service"}>
            <option>all</option><option>debug</option><option>info</option><option>warn</option><option>error</option>
          </select>
        </label>
        <label className="shp-toggle" data-on={follow}>
          <span className="shp-toggle__sw" />
          <input
            type="checkbox"
            checked={follow}
            onChange={e => setFollow(e.target.checked)}
            style={{
              position: "absolute",
              opacity: 0,
              width: 1,
              height: 1,
              margin: -1,
              padding: 0,
              overflow: "hidden",
              clip: "rect(0,0,0,0)",
              whiteSpace: "nowrap",
              border: 0,
            }}
          />
          Follow
        </label>
        <input className="shp-input" placeholder="Search…" value={search} onChange={e => setSearch(e.target.value)} />
      </div>
      {stream === "service" ? (
        <div className="shp-table-wrap">
          <table className="shp-table shp-logs-table">
            <thead><tr><th className="col-time">Time</th><th className="col-level">Level</th><th>Message</th></tr></thead>
            <tbody>
              {filtered.map((l, i) => l.record && (
                <tr key={i} onClick={() => setSelected(l)} data-selected={selected === l}>
                  <td className="col-time">{String(l.record.time || "")}</td>
                  <td className="col-level"><span className="shp-level-pill" data-level={String(l.record.level || "info").toLowerCase()}>{String(l.record.level || "")}</span></td>
                  <td>{String(l.record.msg || "")}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <pre className="shp-mono-view">
          {filtered.map((l, i) => <div key={i}>{l.raw}</div>)}
        </pre>
      )}
      <div ref={endRef} />
      {selected?.record && (
        <pre className="shp-mono-view" style={{ height: "auto", maxHeight: 200, marginTop: 12 }}>
          {JSON.stringify(selected.record, null, 2)}
        </pre>
      )}
      <div className="shp-btn-row" style={{ marginTop: 12 }}>
        <Button variant="ghost" onClick={() => OpenLogsFolder()}>Open logs folder</Button>
      </div>
    </>
  );
}
