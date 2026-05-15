import { useEffect, useState } from "react";
import { Button } from "../components/Button";
import { Lamp } from "../components/Lamp";
import { Help } from "../components/Help";
import { UpdateState, type ButtonStatePayload, type LampWhich, type Tone, type UpdateStatePayload } from "../types";
import {
  InstallService, UninstallService, RestartService,
  DownloadUpdate, CancelDownload, InstallUpdate, OpenReleaseNotes,
  RelaunchPanel,
} from "../wails/go/main/App";
import { EventsEmit } from "../wails/runtime/runtime";

type Lamps = Record<LampWhich, { tone: Tone; label: string; sub?: string }>;

interface Props {
  lamps: Lamps;
  buttons: ButtonStatePayload;
  update: UpdateStatePayload;
  configDirty?: boolean;
}

function updateTone(s: UpdateState): "green" | "red" | "blue" | undefined {
  if (s === UpdateState.Installed) return "green";
  if (s === UpdateState.DownloadFailed || s === UpdateState.InstallFailed) return "red";
  if (s === UpdateState.Available || s === UpdateState.Downloading || s === UpdateState.Ready || s === UpdateState.Installing) return "blue";
  return undefined;
}

export function StatusTab({ lamps, buttons, update, configDirty }: Props) {
  const [busy, setBusy] = useState(false);

  // When the install pipeline reaches Installed, ask the Go side to
  // spawn the new exe and quit this one. A brief delay lets the
  // operator see the success row before the window goes away.
  useEffect(() => {
    if (update.state !== UpdateState.Installed) return;
    const t = window.setTimeout(() => { RelaunchPanel(); }, 1200);
    return () => window.clearTimeout(t);
  }, [update.state]);

  const adminAction = async (fn: () => Promise<{ ok: boolean; error_message?: string }>, isRestart = false) => {
    setBusy(true);
    try {
      const res = await fn();
      if (isRestart && res.ok && configDirty) {
        EventsEmit("footer:set", { kind: "info", text: "Note: unsaved config changes were not applied." });
      }
    } finally { setBusy(false); }
  };

  const installed = !buttons.install && (buttons.uninstall || buttons.restart);
  const notInstalled = buttons.install && !buttons.uninstall && !buttons.restart;
  const hint = busy
    ? "Working — re-checking lamps…"
    : installed
      ? "↑ All service actions require admin privileges"
      : notInstalled
        ? "↑ Install requires admin privileges"
        : "";

  return (
    <>
      <div className="shp-h">Service health</div>
      <section className="shp-lamps">
        <Lamp name="Local service" tone={lamps.service.tone} label={lamps.service.label} sub={lamps.service.sub}>
          <Help title="Service" what="Local SerialHop Windows service state." />
        </Lamp>
        <Lamp name="Lab-bridge server" tone={lamps.server.tone} label={lamps.server.label} sub={lamps.server.sub}>
          <Help title="Server" what="Reachability + health of the configured lab-bridge server." />
        </Lamp>
        <Lamp name="Reverse tunnel" tone={lamps.tunnel.tone} label={lamps.tunnel.label} sub={lamps.tunnel.sub}>
          <Help title="Tunnel" what="State of this machine's Chisel reverse tunnel into the lab-bridge." />
        </Lamp>
      </section>

      <div className="shp-h">Service control</div>
      <div className="shp-service-actions">
        <Button variant="primary" elevated disabled={busy || !buttons.install} onClick={() => adminAction(InstallService)}>Install</Button>
        <Button variant="danger" elevated disabled={busy || !buttons.uninstall} onClick={() => adminAction(UninstallService)}>Uninstall</Button>
        <Button elevated disabled={busy || !buttons.restart} onClick={() => adminAction(RestartService, true)}>Restart</Button>
        {hint && <span className="shp-service-actions__hint">{hint}</span>}
      </div>

      {update.state !== UpdateState.Idle && (
        <div className="shp-update" data-tone={updateTone(update.state)}>
          <span className="shp-update__tag">Update</span>
          <span className="shp-update__msg"><UpdateLabel update={update} /></span>
          {update.state === UpdateState.Downloading && (
            <div className="shp-update__progressbar" data-indeterminate="true"><i /></div>
          )}
          <div className="shp-update__actions">
            <UpdateButtons
              update={update}
              onDownload={() => DownloadUpdate()}
              onCancel={() => CancelDownload()}
              onInstall={() => InstallUpdate()}
              onReleaseNotes={() => OpenReleaseNotes()}
            />
          </div>
        </div>
      )}
    </>
  );
}

function UpdateLabel({ update }: { update: UpdateStatePayload }) {
  const tag = update.release_tag;
  switch (update.state) {
    case UpdateState.Available:      return <>Version <b>{tag}</b> is available.</>;
    case UpdateState.Downloading:    return <><b>{tag}</b> downloading…</>;
    case UpdateState.DownloadFailed: return <><b>{tag}</b> — download failed.</>;
    case UpdateState.Ready:          return <><b>{tag}</b> ready to install.</>;
    case UpdateState.Installing:     return <>Installing… service will restart automatically.</>;
    case UpdateState.Installed:      return <>Updated to <b>{tag}</b>. Restarting…</>;
    case UpdateState.InstallFailed:  return <>Update failed — service restored to previous version.</>;
    default:                         return null;
  }
}

function UpdateButtons(props: {
  update: UpdateStatePayload;
  onDownload: () => void;
  onCancel: () => void;
  onInstall: () => void;
  onReleaseNotes: () => void;
}) {
  const s = props.update.state;
  return (
    <>
      {s === UpdateState.Available && <>
        <Button onClick={props.onReleaseNotes}>Release notes</Button>
        <Button variant="primary" onClick={props.onDownload}>Download</Button>
      </>}
      {s === UpdateState.Downloading && <Button variant="danger" onClick={props.onCancel}>Cancel</Button>}
      {s === UpdateState.DownloadFailed && <Button onClick={props.onDownload}>Retry</Button>}
      {s === UpdateState.Ready && <>
        <Button onClick={props.onReleaseNotes}>Release notes</Button>
        <Button variant="primary" elevated onClick={props.onInstall}>Install update</Button>
      </>}
      {s === UpdateState.InstallFailed && <Button elevated onClick={props.onInstall}>Retry</Button>}
    </>
  );
}
