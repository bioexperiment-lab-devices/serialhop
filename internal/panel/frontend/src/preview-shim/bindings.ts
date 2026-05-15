// In-memory fakes for window.go.main.App. The SPA's binding wrapper at
// src/wails/go/main/App.ts dispatches every call through this object, so
// methods must be named exactly as exported there.

import type { FieldErrorDTO } from "../types";
import { store, fakeDevices, fakePorts } from "./seed";
import { emit } from "./events";

const delay = (ms: number) => new Promise<void>(r => setTimeout(r, ms));
const ok = { ok: true };

export const App: Record<string, (...args: any[]) => Promise<any>> = {
  GetVersion: async () => "0.14.4-preview",

  LoadConfigFromDisk: async () => structuredClone(store.config),

  ValidateConfig: async (cfg: any): Promise<FieldErrorDTO[]> => {
    const errs: FieldErrorDTO[] = [];
    if (!cfg?.lab_bridge?.user) errs.push({ field: "lab_bridge.user", detail: "Username is required." });
    if (!cfg?.lab_bridge?.pass) errs.push({ field: "lab_bridge.pass", detail: "Password is required." });
    return errs;
  },

  SaveConfig: async (cfg: any) => {
    await delay(200);
    store.config = structuredClone(cfg);
    emit("footer:set", { kind: "ok", text: "Saved.", time: new Date().toISOString() });
    return ok;
  },

  VerifyCredentials: async (_host: string, user: string, pass: string) => {
    await delay(150);
    if (user === "bad") return { outcome: "unauthorized", detail: "rejected" };
    if (!user || !pass) return { outcome: "unauthorized", detail: "blank" };
    return { outcome: "ok" };
  },

  OpenConfigInEditor: async () => { console.info("[preview] would open config in editor"); },
  OpenLogsFolder:     async () => { console.info("[preview] would open logs folder"); },
  OpenReleaseNotes:   async () => { console.info("[preview] would open release notes"); },
  PickBackupDir:      async () => "C:/ProgramData/SerialHop/backups",

  InstallService: async () => { await delay(200); emit("footer:set", { kind: "ok", text: "Service installed.", time: new Date().toISOString() }); return ok; },
  UninstallService: async () => { await delay(200); emit("footer:set", { kind: "ok", text: "Service uninstalled.", time: new Date().toISOString() }); return ok; },
  RestartService: async () => { await delay(200); emit("footer:set", { kind: "ok", text: "Service restarted.", time: new Date().toISOString() }); return ok; },

  TriggerProbe: async () => {},
  CheckForUpdate: async () => {},
  DownloadUpdate: async () => {},
  CancelDownload: async () => {},
  InstallUpdate: async () => ok,

  GetDevices: async () => ({
    devices: structuredClone(fakeDevices),
    discovered_at: new Date().toISOString(),
    status: { reachable: store.lamps.service.tone !== "red" },
  }),
  Discover: async () => ({
    devices: structuredClone(fakeDevices),
    discovered_at: new Date().toISOString(),
    status: { reachable: store.lamps.service.tone !== "red" },
  }),
  DisconnectAll: async () => ok,
  GetPorts: async () => ({
    ports: structuredClone(fakePorts),
    status: { reachable: store.lamps.service.tone !== "red" },
  }),

  Diagnostics: async () => ({
    panel_version: "preview",
    cache_path: "C:/ProgramData/SerialHop/server-info.cache.json",
    cache_exists: true,
    cache_user: "preview-user",
    cache_fetched_at: new Date().toISOString(),
    cache_actual_rest_port: 49283,
    base_url_resolved: "http://127.0.0.1:49283",
    base_url_status: "ok",
    configured_lab_bridge_user: "preview-user",
    config_path: "C:/ProgramData/SerialHop/SerialHop_config.yaml",
    data_dir: "C:/ProgramData/SerialHop",
    panel_error_log_path: "C:/ProgramData/SerialHop/logs/SerialHop_panel_error.log",
  }),

  StartLogStream: async (id: string) => {
    store.activeLogStream = id as any;
    // Synthesize fake backlog so the preview exercises the production
    // seed-from-return-value codepath. The "service" stream mirrors the
    // record-shaped payload the real Go side returns; the other streams
    // are raw text.
    if (id === "service") {
      const now = Date.now();
      return [0, 1, 2].map(i => ({
        stream: "service",
        record: {
          time: new Date(now - (3 - i) * 1500).toISOString(),
          level: "info",
          msg: `(backlog) replay line ${i + 1}`,
        },
      }));
    }
    return [
      { stream: id, raw: `[preview] backlog line #1 for ${id}` },
      { stream: id, raw: `[preview] backlog line #2 for ${id}` },
    ];
  },
  StopLogStream: async () => {
    store.activeLogStream = null;
  },

  RecordFrontendCrash: async (message: string, source: string, _stack: string) => {
    console.info("[preview] RecordFrontendCrash", { message, source });
  },
};
