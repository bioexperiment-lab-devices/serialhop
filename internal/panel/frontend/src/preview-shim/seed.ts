// Seed data and a tiny in-memory store for the macOS preview shim.
// Mirrors the DTOs the SPA expects, with realistic-looking values.
// VITE_PREVIEW=1 only — never bundled into the Wails-targeted build.

import type { ButtonStatePayload, FooterPayload, LampWhich, Tone, UpdateStatePayload } from "../types";
import { UpdateState } from "../types";

export type ScenarioId =
  | "default"
  | "service-stopped"
  | "config-invalid"
  | "update-available"
  | "downloading-update";

export interface ConfigShape {
  lab_bridge: { host: string; user: string; pass: string };
  rest: { port: number };
  discovery: { include: string[]; exclude: string[]; post_open_settle_ms: number };
  log: { level: string };
  auto_update: { enabled: boolean };
  flashing: { enabled: boolean; backup_dir: string; keep_n: number };
}

export const defaultConfig: ConfigShape = {
  lab_bridge: { host: "111.88.145.138", user: "preview-user", pass: "preview-pass" },
  rest: { port: 0 },
  discovery: { include: [], exclude: [], post_open_settle_ms: 2000 },
  log: { level: "info" },
  auto_update: { enabled: true },
  flashing: { enabled: false, backup_dir: "", keep_n: 10 },
};

export const fakeDevices = [
  { id: "petri-A", type: "Petri Camera", type_code: 1, port: "COM3" },
  { id: "incubator-B", type: "Incubator", type_code: 2, port: "COM4" },
  { id: "balance-C", type: "Balance", type_code: 3, port: "COM7" },
];

export const fakePorts = [
  { name: "COM3", is_usb: true, vid: "2341", pid: "0043", serial_number: "AB-001", product: "Arduino Uno", discovered: true, device_id: "petri-A" },
  { name: "COM4", is_usb: true, vid: "1A86", pid: "7523", serial_number: "",       product: "CH340",      discovered: true, device_id: "incubator-B" },
  { name: "COM7", is_usb: true, vid: "10C4", pid: "EA60", serial_number: "C-3201", product: "Silicon Labs CP210x", discovered: true, device_id: "balance-C" },
  { name: "COM1", is_usb: false, vid: "",    pid: "",    serial_number: "",       product: "",           discovered: false },
];

interface Store {
  config: ConfigShape;
  scenario: ScenarioId;
  lamps: Record<LampWhich, { tone: Tone; label: string; sub?: string }>;
  buttons: ButtonStatePayload;
  warn: string | null;
  footer: FooterPayload | null;
  update: UpdateStatePayload;
  activeLogStream: "service" | "stderr" | "panel" | null;
  keepAwakeActive: boolean;
}

export const store: Store = {
  config: structuredClone(defaultConfig),
  scenario: "default",
  lamps: {
    service: { tone: "green", label: "Running", sub: "Up since 09:14" },
    server:  { tone: "green", label: "Reachable", sub: "118 ms" },
    tunnel:  { tone: "green", label: "Connected", sub: "RTT 33 ms" },
  },
  buttons: { install: false, uninstall: true, restart: true },
  warn: null,
  footer: { kind: "ok", text: "All systems nominal.", time: new Date().toISOString() },
  update: { state: UpdateState.Idle, release_tag: "" },
  activeLogStream: null,
  keepAwakeActive: false,
};

export function applyScenario(s: ScenarioId): void {
  store.scenario = s;
  switch (s) {
    case "default":
      store.lamps.service = { tone: "green", label: "Running" };
      store.lamps.server  = { tone: "green", label: "Up", sub: "111.88.145.138" };
      store.lamps.tunnel  = { tone: "green", label: "Connected", sub: "remote port 29017" };
      store.buttons = { install: false, uninstall: true, restart: true };
      store.warn = null;
      store.update = { state: UpdateState.Idle, release_tag: "" };
      break;
    case "service-stopped":
      store.lamps.service = { tone: "red", label: "Stopped" };
      store.lamps.server  = { tone: "yellow", label: "Reachable (service down)" };
      store.lamps.tunnel  = { tone: "red", label: "Disconnected" };
      store.buttons = { install: true, uninstall: false, restart: false };
      break;
    case "config-invalid":
      store.warn = "⚠ Config file is malformed (line 12: unexpected indentation).";
      store.lamps.service = { tone: "red", label: "Not installed", sub: "config invalid" };
      break;
    case "update-available":
      store.update = { state: UpdateState.Available, release_tag: "v0.15.0" };
      break;
    case "downloading-update":
      store.update = { state: UpdateState.Downloading, release_tag: "v0.15.0" };
      break;
  }
}
