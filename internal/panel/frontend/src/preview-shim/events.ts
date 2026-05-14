// In-memory implementation of window.runtime. Mirrors the surface used by
// src/wails/runtime/runtime.ts: EventsOn, EventsOff, EventsEmit.
//
// Two preview-only behaviors that differ from the real Wails runtime in
// useful ways:
//   - EventsOn replays the last-emitted value for sticky events (lamps,
//     footer, buttons, update, warn) so subscribers that mount after the
//     initial emit still see their state.
//   - EventsOn returns a cleanup that removes only the specific callback,
//     not every subscriber for the event name. Protects against latent
//     unsubscribe-stomps if multiple components ever share an event.

import { store, fakeDevices } from "./seed";
import type { LogLinePayload } from "../types";

type Listener = (...data: any[]) => void;

const listeners = new Map<string, Set<Listener>>();
const lastEmitted = new Map<string, unknown[]>();

const STICKY_EVENTS = new Set([
  "status:lamp",
  "buttons:state",
  "footer:set",
  "update:state",
  "warn:set",
  "warn:clear",
]);

export const runtime = {
  EventsOn(name: string, cb: Listener) {
    if (!listeners.has(name)) listeners.set(name, new Set());
    listeners.get(name)!.add(cb);
    // Replay last value for sticky events so late subscribers don't miss
    // initial state. Fire on a microtask so the caller's render finishes
    // before the listener runs (matches the timing developers expect from
    // event-bus subscriptions).
    if (STICKY_EVENTS.has(name) && lastEmitted.has(name)) {
      const args = lastEmitted.get(name)!;
      queueMicrotask(() => { try { cb(...args); } catch (e) { console.error("[preview] replay error:", e); } });
    }
    return () => {
      listeners.get(name)?.delete(cb);
    };
  },
  EventsOff(...names: string[]) {
    for (const n of names) listeners.delete(n);
  },
  EventsEmit(name: string, ...data: any[]) {
    emit(name, ...data);
  },
};

export function emit(name: string, ...data: any[]): void {
  if (STICKY_EVENTS.has(name)) lastEmitted.set(name, data);
  const set = listeners.get(name);
  if (!set) return;
  for (const cb of [...set]) {
    try { cb(...data); } catch (e) { console.error("[preview] listener error:", e); }
  }
}

let started = false;

export function startSimulator(): void {
  if (started) return;
  started = true;

  // Periodic log lines while a service stream is active.
  let logSeq = 0;
  setInterval(() => {
    if (store.activeLogStream !== "service") return;
    const device = fakeDevices[logSeq % fakeDevices.length];
    const line: LogLinePayload = {
      stream: "service",
      record: {
        time: new Date().toISOString(),
        level: ["info", "info", "info", "warn", "error"][logSeq % 5],
        msg: `heartbeat from ${device.id}`,
        device_id: device.id,
        port: device.port,
      },
    };
    emit("log:line", line);
    logSeq++;
  }, 1500);

  // Seed the lastEmitted map with the initial store state so subscribers
  // that mount later receive correct initial values via the replay path.
  primeInitial();
}

function primeInitial(): void {
  for (const which of ["service", "server", "tunnel"] as const) {
    emit("status:lamp", { which, ...store.lamps[which] });
  }
  emit("buttons:state", store.buttons);
  if (store.warn) emit("warn:set", store.warn); else emit("warn:clear");
  if (store.footer) emit("footer:set", store.footer);
  emit("update:state", store.update);
}

export function resyncAll(): void {
  primeInitial();
}
