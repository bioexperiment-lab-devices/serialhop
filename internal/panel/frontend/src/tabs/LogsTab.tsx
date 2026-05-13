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
    <div className="logs-tab">
      <div className="logs-controls">
        <label>
          Stream:
          <select value={stream} onChange={e => setStream(e.target.value as StreamID)}>
            <option value="service">Service log</option>
            <option value="stderr">Stderr</option>
            <option value="panel">Panel errors</option>
          </select>
          <Help title={`${stream} stream`} what="Source file for the displayed log entries." />
        </label>
        <label>
          Level:
          <select value={level} onChange={e => setLevel(e.target.value as LevelFilter)} disabled={stream !== "service"}>
            <option>all</option><option>debug</option><option>info</option><option>warn</option><option>error</option>
          </select>
        </label>
        <label>
          <input type="checkbox" checked={follow} onChange={e => setFollow(e.target.checked)} /> Follow
        </label>
        <input className="logs-search" placeholder="Search…" value={search} onChange={e => setSearch(e.target.value)} />
      </div>
      <div className="logs-view">
        {stream === "service" ? (
          <table className="logs-table">
            <thead><tr><th>Time</th><th>Level</th><th>Message</th></tr></thead>
            <tbody>
              {filtered.map((l, i) => l.record && (
                <tr key={i} onClick={() => setSelected(l)} data-selected={selected === l}>
                  <td>{String(l.record.time || "")}</td>
                  <td>{String(l.record.level || "")}</td>
                  <td>{String(l.record.msg || "")}</td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <pre className="logs-raw">
            {filtered.map((l, i) => <div key={i}>{l.raw}</div>)}
          </pre>
        )}
        <div ref={endRef} />
      </div>
      {selected?.record && (
        <pre className="logs-details">{JSON.stringify(selected.record, null, 2)}</pre>
      )}
      <div className="logs-actions">
        <Button variant="ghost" onClick={() => OpenLogsFolder()}>Open logs folder</Button>
      </div>
    </div>
  );
}
