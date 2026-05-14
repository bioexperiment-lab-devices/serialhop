import { useState } from "react";
import { Diagnostics } from "../wails/go/main/App";

// DiagnosticsDetails is a collapsed-by-default <details> block that
// fetches and prints the panel's current reachability state when
// expanded. Rendered next to the "Can't reach the local service"
// empty-state so operators can paste the JSON into bug reports
// without grep'ing %ProgramData%\SerialHop\logs\.
export function DiagnosticsDetails() {
  const [data, setData] = useState<Record<string, unknown> | null>(null);
  const [err, setErr] = useState<string | null>(null);

  const onToggle = (e: React.SyntheticEvent<HTMLDetailsElement>) => {
    if (!e.currentTarget.open || data) return;
    Diagnostics()
      .then(d => { setData(d); setErr(null); })
      .catch(e => setErr(String(e)));
  };

  return (
    <details onToggle={onToggle} style={{ marginTop: 12, fontSize: "0.85em" }}>
      <summary style={{ cursor: "pointer", userSelect: "none" }}>Show diagnostics</summary>
      {err && <pre style={{ color: "var(--shp-tone-red, #b91c1c)" }}>{err}</pre>}
      {data && (
        <pre className="shp-mono-view" style={{ maxHeight: 280, marginTop: 8 }}>
          {JSON.stringify(data, null, 2)}
        </pre>
      )}
    </details>
  );
}
