// In-memory implementation of window.runtime. Mirrors the surface used by
// src/wails/runtime/runtime.ts: EventsOn, EventsOff, EventsEmit.

import { store, fakeDevices } from "./seed";
import type { LogLinePayload } from "../types";

type Listener = (...data: any[]) => void;
const listeners = new Map<string, Set<Listener>>();

export const runtime = {
  EventsOn(name: string, cb: Listener) {
    if (!listeners.has(name)) listeners.set(name, new Set());
    listeners.get(name)!.add(cb);
    return () => runtime.EventsOff(name);
  },
  EventsOff(...names: string[]) {
    for (const n of names) listeners.delete(n);
  },
  EventsEmit(name: string, ...data: any[]) {
    emit(name, ...data);
  },
};

export function emit(name: string, ...data: any[]): void {
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

  // Emit initial state once any listener attaches. Use a microtask so
  // App.tsx has time to subscribe.
  queueMicrotask(() => {
    emitInitial();
  });

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
}

function emitInitial(): void {
  for (const which of ["service", "server", "tunnel"] as const) {
    emit("status:lamp", { which, ...store.lamps[which] });
  }
  emit("buttons:state", store.buttons);
  if (store.warn) emit("warn:set", store.warn); else emit("warn:clear");
  if (store.footer) emit("footer:set", store.footer);
  emit("update:state", store.update);
}

export function resyncAll(): void { emitInitial(); }
