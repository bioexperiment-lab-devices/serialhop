import { useEffect, useState } from "react";
import { Button } from "../components/Button";
import { Lamp } from "../components/Lamp";
import { Help } from "../components/Help";
import { UpdateState, type ButtonStatePayload, type LampWhich, type Tone, type UpdateStatePayload } from "../types";
import {
  InstallService, UninstallService, RestartService,
  DownloadUpdate, CancelDownload, InstallUpdate, OpenReleaseNotes,
} from "../wails/go/main/App";
import { EventsOn, EventsOff, EventsEmit } from "../wails/runtime/runtime";

type Lamps = Record<LampWhich, { tone: Tone; label: string; sub?: string }>;

interface Props { lamps: Lamps; buttons: ButtonStatePayload; configDirty?: boolean; }

export function StatusTab({ lamps, buttons, configDirty }: Props) {
  const [update, setUpdate] = useState<UpdateStatePayload>({ state: UpdateState.Idle, release_tag: "" });
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    const h = (p: UpdateStatePayload) => setUpdate(p);
    EventsOn("update:state", h);
    return () => EventsOff("update:state");
  }, []);

  const adminAction = async (fn: () => Promise<{ ok: boolean; error_message?: string }>, isRestart = false) => {
    setBusy(true);
    try {
      const res = await fn();
      if (isRestart && res.ok && configDirty) {
        EventsEmit("footer:set", { kind: "info", text: "Note: unsaved config changes were not applied." });
      }
    } finally { setBusy(false); }
  };

  return (
    <div className="status-tab">
      <section className="lamps">
        <Lamp name="Service" tone={lamps.service.tone} label={lamps.service.label} sub={lamps.service.sub}>
          <Help title="Service" what="Local SerialHop Windows service state." />
        </Lamp>
        <Lamp name="Server" tone={lamps.server.tone} label={lamps.server.label} sub={lamps.server.sub}>
          <Help title="Server" what="Reachability + health of the configured lab-bridge server." />
        </Lamp>
        <Lamp name="Tunnel" tone={lamps.tunnel.tone} label={lamps.tunnel.label} sub={lamps.tunnel.sub}>
          <Help title="Tunnel" what="State of this machine's Chisel reverse tunnel into the lab-bridge." />
        </Lamp>
      </section>

      <section className="actions">
        <Button elevated disabled={busy || !buttons.install} onClick={() => adminAction(InstallService)}>Install</Button>
        <Button elevated disabled={busy || !buttons.uninstall} onClick={() => adminAction(UninstallService)}>Uninstall</Button>
        <Button elevated disabled={busy || !buttons.restart} onClick={() => adminAction(RestartService, true)}>Restart</Button>
      </section>

      {update.state !== UpdateState.Idle && (
        <section className="update-row">
          <UpdateLabel update={update} />
          <UpdateButtons
            update={update}
            onDownload={() => DownloadUpdate()}
            onCancel={() => CancelDownload()}
            onInstall={() => InstallUpdate()}
            onReleaseNotes={() => OpenReleaseNotes()}
          />
        </section>
      )}
    </div>
  );
}

function UpdateLabel({ update }: { update: UpdateStatePayload }) {
  const text: Record<UpdateState, string> = {
    [UpdateState.Idle]: "",
    [UpdateState.Available]: `Update: ${update.release_tag} available`,
    [UpdateState.Downloading]: `Update: ${update.release_tag} — downloading…`,
    [UpdateState.DownloadFailed]: `Update: ${update.release_tag} — download failed`,
    [UpdateState.Ready]: `Update: ${update.release_tag} — ready to install`,
    [UpdateState.Installing]: "Update: installing…",
    [UpdateState.Installed]: `Updated to ${update.release_tag}. Close and reopen this window to load the new panel.`,
    [UpdateState.InstallFailed]: "Update failed — service restored to previous version.",
  };
  const color =
    update.state === UpdateState.DownloadFailed || update.state === UpdateState.InstallFailed ? "red"
    : update.state === UpdateState.Installed ? "green"
    : "default";
  return <span data-color={color}>{text[update.state]}</span>;
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
    <div className="update-buttons">
      {s === UpdateState.Available && <>
        <Button variant="primary" onClick={props.onDownload}>Download</Button>
        <Button variant="ghost" onClick={props.onReleaseNotes}>Release notes</Button>
      </>}
      {s === UpdateState.Downloading && <Button variant="ghost" onClick={props.onCancel}>Cancel</Button>}
      {s === UpdateState.DownloadFailed && <Button variant="primary" onClick={props.onDownload}>Retry</Button>}
      {s === UpdateState.Ready && <>
        <Button variant="primary" elevated onClick={props.onInstall}>Install update</Button>
        <Button variant="ghost" onClick={props.onReleaseNotes}>Release notes</Button>
      </>}
      {s === UpdateState.InstallFailed && <Button variant="primary" elevated onClick={props.onInstall}>Retry</Button>}
    </div>
  );
}
