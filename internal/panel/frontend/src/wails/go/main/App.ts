// Type stubs that delegate to Wails' runtime globals at execution time.
// `wails build` regenerates equivalent files at src/wails/wailsjs/ (which
// tsc skips via tsconfig.json's exclude). We keep these committed so:
//
//   1. `tsc --noEmit` passes in PR CI without running `wails generate
//      module` (which requires package main at the repo root and a
//      build-tagged wails.Run call — not how this project is wired).
//   2. The bundled JS produced by `vite build` actually calls into Wails
//      at runtime, by routing every binding through `window.go.main.App.*`.
//
// When you add or change a Go-side binding in internal/panel/bindings.go,
// update the matching declaration here.

/* eslint-disable @typescript-eslint/no-explicit-any */

interface WailsAppGlobal {
  [method: string]: (...args: unknown[]) => Promise<unknown>;
}

function app(): WailsAppGlobal {
  const w = window as unknown as { go?: { main?: { App?: WailsAppGlobal } } };
  if (!w.go?.main?.App) {
    throw new Error("Wails runtime not available: window.go.main.App is missing");
  }
  return w.go.main.App;
}

function call<T>(name: string, ...args: unknown[]): Promise<T> {
  return app()[name](...args) as Promise<T>;
}

export function GetVersion(): Promise<string> { return call<string>("GetVersion"); }
export function LoadConfigFromDisk(): Promise<any> { return call<any>("LoadConfigFromDisk"); }
export function ValidateConfig(cfg: any): Promise<Array<{ field: string; detail: string }>> { return call("ValidateConfig", cfg); }
export function SaveConfig(cfg: any): Promise<{ ok: boolean; field_errors?: Array<{ field: string; detail: string }> }> { return call("SaveConfig", cfg); }
export function VerifyCredentials(host: string, user: string, pass: string): Promise<{ outcome: string; detail?: string }> { return call("VerifyCredentials", host, user, pass); }
export function OpenConfigInEditor(): Promise<void> { return call("OpenConfigInEditor"); }
export function OpenLogsFolder(): Promise<void> { return call("OpenLogsFolder"); }
export function OpenReleaseNotes(): Promise<void> { return call("OpenReleaseNotes"); }
export function PickBackupDir(): Promise<string> { return call<string>("PickBackupDir"); }

export function InstallService(): Promise<{ ok: boolean; error_message?: string; cancelled?: boolean }> { return call("InstallService"); }
export function UninstallService(): Promise<{ ok: boolean; error_message?: string; cancelled?: boolean }> { return call("UninstallService"); }
export function RestartService(): Promise<{ ok: boolean; error_message?: string; cancelled?: boolean }> { return call("RestartService"); }

export function TriggerProbe(which: string): Promise<void> { return call("TriggerProbe", which); }
export function CheckForUpdate(): Promise<void> { return call("CheckForUpdate"); }
export function DownloadUpdate(): Promise<void> { return call("DownloadUpdate"); }
export function CancelDownload(): Promise<void> { return call("CancelDownload"); }
export function InstallUpdate(): Promise<{ ok: boolean; error_message?: string; cancelled?: boolean }> { return call("InstallUpdate"); }

export function GetKeepAwake(): Promise<{ active: boolean; reachable: boolean; reason?: string; error_message?: string }> {
  return call("GetKeepAwake");
}
export function EnableKeepAwake(): Promise<{ active: boolean; reachable: boolean; reason?: string; error_message?: string }> {
  return call("EnableKeepAwake");
}
export function DisableKeepAwake(): Promise<{ active: boolean; reachable: boolean; reason?: string; error_message?: string }> {
  return call("DisableKeepAwake");
}

// RelaunchPanel spawns a detached copy of the current panel exe and
// quits this one. The OS file at os.Executable() is the freshly
// installed exe after a successful update; the running panel is still
// the previous version, so the spawn-then-quit dance is what brings
// the new UI up without asking the operator to reopen the window.
export function RelaunchPanel(): Promise<void> { return call("RelaunchPanel"); }

export function GetDevices(): Promise<any> { return call<any>("GetDevices"); }
export function Discover(): Promise<any> { return call<any>("Discover"); }
export function DisconnectAll(): Promise<any> { return call<any>("DisconnectAll"); }
export function DisconnectPort(port: string): Promise<any> { return call<any>("DisconnectPort", port); }
export function GetPorts(): Promise<any> { return call<any>("GetPorts"); }

// Diagnostics returns a snapshot of every input that gates the
// Devices/Ports reachability check (cache contents, derived port URL,
// configured user, log path). Bound for "Can't reach the local
// service" reports so operators can paste a single JSON blob instead
// of hunting under %ProgramData%\SerialHop\logs\.
export function Diagnostics(): Promise<Record<string, unknown>> { return call<Record<string, unknown>>("Diagnostics"); }

// StartLogStream attaches the Go-side tailer to the given stream and
// returns the most recent backlog lines (oldest first). The frontend
// seeds its in-memory buffer with these before subscribing to live
// log:line events — see internal/panel/bindings.go for the rationale.
export function StartLogStream(id: string): Promise<unknown[]> { return call("StartLogStream", id); }
export function StopLogStream(): Promise<void> { return call("StopLogStream"); }

// RecordFrontendCrash appends one JSON line to the panel crash journal.
// Called by the React ErrorBoundary fallback and the global window.error
// / unhandledrejection listeners in main.tsx. The Go side swallows all
// failures, so this promise resolves to undefined except in the rare
// case where the Wails bridge itself rejects (e.g. binding missing).
export function RecordFrontendCrash(message: string, source: string, stack: string): Promise<void> {
  return call("RecordFrontendCrash", message, source, stack);
}
