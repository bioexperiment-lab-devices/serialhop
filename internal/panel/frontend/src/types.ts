export type Tone = "green" | "yellow" | "red" | "grey";
export type LampWhich = "service" | "server" | "tunnel";
export type FooterKind = "ok" | "work" | "err" | "info";

export interface LampPayload {
  which: LampWhich;
  tone: Tone;
  label: string;
  sub?: string;
}

export interface FooterPayload {
  kind: FooterKind;
  text: string;
  time?: string;
  progress?: number;
}

export type StreamID = "service" | "stderr" | "panel";

export interface LogLinePayload {
  stream: StreamID;
  raw?: string;
  record?: Record<string, unknown>;
}

// UpdateState mirrors internal/panel/update_state.go.
export enum UpdateState {
  Idle = 0,
  Available = 1,
  Downloading = 2,
  DownloadFailed = 3,
  Ready = 4,
  Installing = 5,
  Installed = 6,
  InstallFailed = 7,
}

export interface UpdateStatePayload {
  state: UpdateState;
  release_tag: string;
}

export interface FieldErrorDTO {
  field: string;
  detail: string;
}

export interface ButtonStatePayload {
  install: boolean;
  uninstall: boolean;
  restart: boolean;
}

export interface KeepAwakePayload {
  active: boolean;
  reachable: boolean;
  reason?: string;          // "service_down" | "unreachable" | undefined
  error_message?: string;
}
