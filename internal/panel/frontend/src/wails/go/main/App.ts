// Hand-written placeholders — replaced by Wails-generated bindings during
// `wails build` (release) or `wails dev` (local dev). Committed so that
// `tsc --noEmit` passes in PR CI without running `wails generate module`
// (which requires package main at the repo root; ours is nested under cmd/).
// See internal/panel/frontend/src/wails/README.md for full rationale.

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export async function GetVersion(): Promise<string> { return ""; }

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export async function LoadConfigFromDisk(): Promise<any> { return {}; }

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export async function ValidateConfig(_cfg: any): Promise<Array<{ field: string; detail: string }>> { return []; }
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export async function SaveConfig(_cfg: any): Promise<{ ok: boolean; field_errors?: Array<{ field: string; detail: string }> }> { return { ok: true }; }
export async function VerifyCredentials(_host: string, _user: string, _pass: string): Promise<{ outcome: string; detail?: string }> { return { outcome: "skipped" }; }
export async function OpenConfigInEditor(): Promise<void> {}
export async function OpenLogsFolder(): Promise<void> {}
export async function OpenReleaseNotes(): Promise<void> {}
export async function PickBackupDir(): Promise<string> { return ""; }

export async function InstallService(): Promise<{ ok: boolean; error_message?: string; cancelled?: boolean }> { return { ok: false }; }
export async function UninstallService(): Promise<{ ok: boolean; error_message?: string; cancelled?: boolean }> { return { ok: false }; }
export async function RestartService(): Promise<{ ok: boolean; error_message?: string; cancelled?: boolean }> { return { ok: false }; }

export async function TriggerProbe(_which: string): Promise<void> {}
export async function CheckForUpdate(): Promise<void> {}
export async function DownloadUpdate(): Promise<void> {}
export async function CancelDownload(): Promise<void> {}
export async function InstallUpdate(): Promise<{ ok: boolean; error_message?: string; cancelled?: boolean }> { return { ok: false }; }

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export async function GetDevices(): Promise<any> { return { devices: [], discovered_at: null, status: { reachable: false, reason: "unreachable" } }; }
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export async function Discover(): Promise<any> { return { devices: [], discovered_at: null, status: { reachable: false, reason: "unreachable" } }; }
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export async function DisconnectAll(): Promise<any> { return { released: 0, status: { reachable: false, reason: "unreachable" } }; }
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export async function GetPorts(): Promise<any> { return { ports: [], status: { reachable: false, reason: "unreachable" } }; }

export async function StartLogStream(_id: string): Promise<void> {}
export async function StopLogStream(): Promise<void> {}
