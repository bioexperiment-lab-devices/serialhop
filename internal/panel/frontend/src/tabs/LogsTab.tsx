import { Fragment, useEffect, useState } from "react";
import { Button } from "../components/Button";
import { Help } from "../components/Help";
import { OpenLogsFolder } from "../wails/go/main/App";
import type { LogLinePayload, StreamID } from "../types";
import type { LogStreamState } from "../state/globalStore";

type LevelFilter = "all" | "debug" | "info" | "warn" | "error";

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

const LABEL_STYLE: React.CSSProperties = { fontSize: 11.5, fontWeight: 500 };

// LogsTab is now a thin view over the App-level log streaming state. The
// buffer and tailer subscription live in globalStore so they survive both
// tab switches (LogsTab unmount/remount) and stream changes — see
// useGlobalUiState for the lifecycle.
export function LogsTab({ logState }: { logState: LogStreamState }) {
  const { stream, setStream, lines } = logState;
  const [level, setLevel] = useState<LevelFilter>("all");
  const [follow, setFollow] = useState(true);
  const [search, setSearch] = useState("");
  const [selected, setSelected] = useState<LogLinePayload | null>(null);

  useEffect(() => {
    if (!follow) return;
    // Reset the outer scroll container directly: scrollIntoView on a marker
    // inside `.shp-content__pad` lands at scrollTop = padding-top (~18px),
    // which shows as a slight downward offset on streams with short backlogs.
    const scroller = document.querySelector(".shp-content") as HTMLElement | null;
    if (scroller) scroller.scrollTop = 0;
  }, [lines, follow]);

  // Selection is meaningful only for the service-log table. Clear it when
  // the user switches streams so a previously selected service row doesn't
  // linger in state when they come back.
  useEffect(() => { setSelected(null); }, [stream]);

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
  const display = filtered.slice().reverse();

  return (
    <>
      <div className="shp-logs-controls">
        <label className="shp-row" style={{ gap: 6 }}>
          <span className="shp-muted" style={LABEL_STYLE}>Stream</span>
          <select className="shp-select" value={stream} onChange={e => setStream(e.target.value as StreamID)} style={{ width: 160 }}>
            <option value="service">Service log</option>
            <option value="stderr">Stderr</option>
            <option value="panel">Panel errors</option>
          </select>
          <Help title={streamHelp[stream].title} what={streamHelp[stream].what} />
        </label>
        <label className="shp-row" style={{ gap: 6 }}>
          <span className="shp-muted" style={LABEL_STYLE}>Level</span>
          <select
            className="shp-select"
            value={level}
            onChange={e => setLevel(e.target.value as LevelFilter)}
            disabled={stream !== "service"}
            style={{ width: 110 }}
          >
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
        <input
          className="shp-input shp-input--mono"
          placeholder="Search…"
          value={search}
          onChange={e => setSearch(e.target.value)}
          style={{ width: 220, flex: "0 1 220px" }}
        />
        <span className="shp-gap" />
        <Button variant="ghost" onClick={() => OpenLogsFolder()}>Open logs folder ↗</Button>
      </div>
      {stream === "service" ? (
        <div className="shp-table-wrap">
          <table className="shp-table shp-logs-table">
            <thead><tr><th className="col-time">Time</th><th className="col-level">Level</th><th>Message</th></tr></thead>
            <tbody>
              {display.map((l, i) => l.record && (
                <Fragment key={i}>
                  <tr
                    onClick={() => setSelected(prev => (prev === l ? null : l))}
                    data-selected={selected === l}
                  >
                    <td className="col-time">{String(l.record.time || "")}</td>
                    <td className="col-level"><span className="shp-level-pill" data-level={String(l.record.level || "info").toLowerCase()}>{String(l.record.level || "")}</span></td>
                    <td>{highlightSearch(String(l.record.msg || ""), search)}</td>
                  </tr>
                  {selected === l && (
                    <tr className="shp-logs-detail">
                      <td colSpan={3}>
                        <pre className="shp-logs-detail__json">{JSON.stringify(l.record, null, 2)}</pre>
                      </td>
                    </tr>
                  )}
                </Fragment>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <pre className="shp-mono-view shp-logs-mono">
          {display.map((l, i) => <div key={i}>{l.raw}</div>)}
        </pre>
      )}
    </>
  );
}

function highlightSearch(text: string, q: string): React.ReactNode {
  if (!q) return text;
  const i = text.toLowerCase().indexOf(q.toLowerCase());
  if (i === -1) return text;
  return (
    <>
      {text.slice(0, i)}
      <mark
        style={{
          background: "var(--warning-soft)",
          color: "var(--text)",
          padding: "0 2px",
          borderRadius: 2,
        }}
      >
        {text.slice(i, i + q.length)}
      </mark>
      {text.slice(i + q.length)}
    </>
  );
}
