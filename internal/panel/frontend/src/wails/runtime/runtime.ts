// Type stubs that delegate to Wails' runtime globals at execution time.
// `wails build` regenerates equivalent files at src/wails/wailsjs/runtime/
// (skipped by tsconfig). See ../README.md for the rationale.

/* eslint-disable @typescript-eslint/no-explicit-any */

interface WailsRuntime {
  EventsOn(eventName: string, callback: (...data: any[]) => void): () => void;
  EventsOff(...eventNames: string[]): void;
  EventsEmit(eventName: string, ...data: any[]): void;
}

function rt(): WailsRuntime | null {
  const w = window as unknown as { runtime?: WailsRuntime };
  return w.runtime ?? null;
}

export function EventsOn(eventName: string, callback: (...data: any[]) => void): () => void {
  return rt()?.EventsOn(eventName, callback) ?? (() => {});
}

export function EventsOff(...eventNames: string[]): void {
  rt()?.EventsOff(...eventNames);
}

export function EventsEmit(eventName: string, data?: any): void {
  rt()?.EventsEmit(eventName, data);
}
