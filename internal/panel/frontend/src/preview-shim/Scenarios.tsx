import { useState } from "react";
import { applyScenario, store, type ScenarioId } from "./seed";
import { resyncAll } from "./events";

const OPTIONS: { id: ScenarioId; label: string }[] = [
  { id: "default", label: "Default (healthy)" },
  { id: "service-stopped", label: "Service stopped" },
  { id: "config-invalid", label: "Config invalid" },
  { id: "update-available", label: "Update available" },
  { id: "downloading-update", label: "Downloading update" },
];

export function Scenarios() {
  const [s, setS] = useState<ScenarioId>(store.scenario);
  return (
    <div style={{
      position: "fixed", top: 10, right: 14, zIndex: 100,
      background: "var(--surface)", border: "1px solid var(--border-strong)",
      borderRadius: 4, padding: "6px 10px", boxShadow: "var(--shadow-popover)",
      fontFamily: "'IBM Plex Sans', system-ui, sans-serif", fontSize: 12,
    }}>
      <label style={{ display: "flex", gap: 6, alignItems: "center" }}>
        <span style={{ color: "var(--text-muted)" }}>preview:</span>
        <select
          value={s}
          onChange={e => { const v = e.target.value as ScenarioId; setS(v); applyScenario(v); resyncAll(); }}
          style={{ font: "inherit" }}
        >
          {OPTIONS.map(o => <option key={o.id} value={o.id}>{o.label}</option>)}
        </select>
      </label>
    </div>
  );
}
