// Hand-written placeholders — replaced by Wails-generated runtime during
// `wails build` (release) or `wails dev` (local dev). Committed so that
// `tsc --noEmit` passes in PR CI without running `wails generate module`.
// See internal/panel/frontend/src/wails/README.md for full rationale.

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export function EventsOn(eventName: string, callback: (...data: any[]) => void): () => void {
  void eventName;
  void callback;
  return () => {};
}

export function EventsOff(...eventNames: string[]): void {
  void eventNames;
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export function EventsEmit(eventName: string, data?: any): void {
  void eventName;
  void data;
}
