import type { LampWhich, Tone } from "../types";
type Lamps = Record<LampWhich, { tone: Tone; label: string; sub?: string }>;
export function StatusTab({ lamps: _lamps }: { lamps: Lamps }) {
  return <div>Status (todo)</div>;
}
