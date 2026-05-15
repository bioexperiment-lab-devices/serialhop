import React, { forwardRef, useEffect, useImperativeHandle, useState } from "react";
import { Button } from "../components/Button";
import { Field } from "../components/Field";
import { Section } from "../components/Section";
import { Help } from "../components/Help";
import { Checkbox } from "../components/Checkbox";
import { Modal } from "../components/Modal";
import {
  LoadConfigFromDisk, SaveConfig, ValidateConfig, VerifyCredentials,
  OpenConfigInEditor, PickBackupDir, RestartService,
} from "../wails/go/main/App";
import type { FieldErrorDTO } from "../types";

// Mirrors internal/config.Config — keep field names exactly as Wails generates them
// (yaml tags → snake_case). If the Wails-generated TS type changes, update here.
interface ConfigDTO {
  lab_bridge: { host: string; user: string; pass: string };
  rest: { port: number };
  discovery: { include: string[]; exclude: string[]; post_open_settle_ms: number };
  log: { level: string };
  raw_serial: { enabled: boolean };
  auto_update: { enabled: boolean };
  flashing: { enabled: boolean; backup_dir: string; keep_n: number };
}

interface Props { onDirtyChange: (b: boolean) => void; }

export interface ConfigTabHandle {
  /** Runs validation + credential verify + save. Returns true if save completed. */
  save: () => Promise<boolean>;
  /** Resets form to last-loaded state. */
  discard: () => void;
  /** Returns human-readable labels of fields whose value differs from disk. */
  getChangedFields: () => string[];
}

const clone = <T,>(v: T): T => JSON.parse(JSON.stringify(v));

export const ConfigTab = forwardRef<ConfigTabHandle, Props>(function ConfigTab({ onDirtyChange }, ref) {
  const [loaded, setLoaded] = useState<ConfigDTO | null>(null);
  const [form, setForm] = useState<ConfigDTO | null>(null);
  const [errors, setErrors] = useState<FieldErrorDTO[]>([]);
  const [pendingConfirm, setPendingConfirm] = useState<{ detail: string; alsoRestart: boolean } | null>(null);

  useEffect(() => {
    LoadConfigFromDisk().then((cfg: ConfigDTO) => { setLoaded(clone(cfg)); setForm(clone(cfg)); });
  }, []);

  const dirty = !!(loaded && form && JSON.stringify(loaded) !== JSON.stringify(form));
  useEffect(() => { onDirtyChange(dirty); }, [dirty, onDirtyChange]);

  const discard = () => {
    if (loaded) { setForm(clone(loaded)); setErrors([]); }
  };

  useImperativeHandle(ref, () => ({
    save: async () => {
      if (!form) return false;
      const vErrs = await ValidateConfig(form);
      if (vErrs && vErrs.length) { setErrors(vErrs); return false; }
      setErrors([]);
      const verify = await VerifyCredentials(form.lab_bridge.host, form.lab_bridge.user, form.lab_bridge.pass);
      if (verify.outcome === "unauthorized") {
        setErrors([{ field: "lab_bridge.user", detail: "Server rejected these credentials. Check the username and password." }]);
        return false;
      }
      if (verify.outcome === "needs_confirm") {
        // Operator must resolve the confirm modal first — save cannot complete now.
        setPendingConfirm({ detail: verify.detail || "", alsoRestart: false });
        return false;
      }
      const res = await SaveConfig(form);
      if (!res.ok) { setErrors(res.field_errors || []); return false; }
      setLoaded(clone(form));
      return true;
    },
    discard,
    getChangedFields: () => (loaded && form ? diffFieldLabels(loaded, form) : []),
  }), [form, loaded, discard]);

  if (!form || !loaded) return <div>Loading…</div>;

  const setNested = <S extends keyof ConfigDTO, K extends keyof ConfigDTO[S]>(
    sec: S, key: K, v: ConfigDTO[S][K],
  ) => setForm(prev => prev && { ...prev, [sec]: { ...(prev[sec] as object), [key]: v } } as ConfigDTO);

  const credsMissing = !form.lab_bridge.user || !form.lab_bridge.pass;
  const includeActive = form.discovery.include.length > 0;
  const excludeActive = form.discovery.exclude.length > 0;
  const flashOff = !form.flashing.enabled;

  const save = async (alsoRestart: boolean) => {
    const vErrs = await ValidateConfig(form);
    if (vErrs && vErrs.length) { setErrors(vErrs); return; }
    setErrors([]);
    const verify = await VerifyCredentials(form.lab_bridge.host, form.lab_bridge.user, form.lab_bridge.pass);
    if (verify.outcome === "unauthorized") {
      setErrors([{ field: "lab_bridge.user", detail: "Server rejected these credentials. Check the username and password." }]);
      return;
    }
    if (verify.outcome === "needs_confirm") {
      setPendingConfirm({ detail: verify.detail || "", alsoRestart });
      return;
    }
    await doSave(alsoRestart);
  };

  const doSave = async (alsoRestart: boolean) => {
    const res = await SaveConfig(form);
    if (!res.ok) { setErrors(res.field_errors || []); return; }
    setLoaded(clone(form));
    if (alsoRestart) await RestartService();
  };

  return (
    <>
      {credsMissing && (
        <div className="shp-banner" data-tone="info">
          <span className="shp-banner__chip">First launch</span>
          <span>Enter your lab-bridge credentials to enable the service.</span>
        </div>
      )}

      <Section
        title="Lab-bridge"
        helpComponent={<Help title="Lab-bridge" what="The remote server this machine tunnels into so the rest of the lab network can reach the devices on this host." />}
      >
        <Field label="Host" helpComponent={<Help title="Host" what="lab-bridge VPS host." defaultVal="111.88.145.138" />}>
          <input className="shp-input shp-input--mono"
            value={form.lab_bridge.host}
            data-error={!!errFor(errors, "lab_bridge.host") || undefined}
            onChange={e => setNested("lab_bridge", "host", e.target.value)} />
          {errFor(errors, "lab_bridge.host") && <div className="shp-error">{errFor(errors, "lab_bridge.host")}</div>}
        </Field>
        <Field label="Username"
          helpComponent={
            <Help
              title="Username"
              what="The lab-bridge account username that authenticates this client's chisel tunnel and REST calls."
              when="Save will verify these credentials against the lab-bridge."
            />
          }>
          <input className="shp-input"
            value={form.lab_bridge.user}
            data-error={!!errFor(errors, "lab_bridge.user") || undefined}
            onChange={e => setNested("lab_bridge", "user", e.target.value)} />
          {errFor(errors, "lab_bridge.user") && <div className="shp-error">{errFor(errors, "lab_bridge.user")}</div>}
        </Field>
        <Field label="Password"
          helpComponent={
            <Help
              title="Password"
              what="The lab-bridge account password. Stored as plaintext in the YAML (matches the existing convention)."
              when="Save will verify these credentials against the lab-bridge."
            />
          }>
          <input className="shp-input shp-input--mono"
            value={form.lab_bridge.pass}
            data-error={!!errFor(errors, "lab_bridge.pass") || undefined}
            onChange={e => setNested("lab_bridge", "pass", e.target.value)} />
          {errFor(errors, "lab_bridge.pass") && <div className="shp-error">{errFor(errors, "lab_bridge.pass")}</div>}
        </Field>
      </Section>

      <Section
        title="REST"
        helpComponent={<Help title="REST" what="The local HTTP server other lab tools on this machine talk to." />}
      >
        <Field label="Port" helpComponent={<Help title="REST port" what="Local TCP port the SerialHop service binds." defaultVal="0 (OS-assigned)" />}>
          <input className="shp-input shp-input--mono" type="number" min={0} max={65535}
            value={form.rest.port}
            style={{ maxWidth: 120 }}
            onChange={e => setNested("rest", "port", Number(e.target.value) || 0)} />
          <div className="shp-field__hint">0 = OS picks a free port</div>
        </Field>
      </Section>

      <Section
        title="Discovery"
        helpComponent={<Help title="Discovery" what="How the service searches the local serial ports for known device types." />}
      >
        <ListField label="Include"
          values={form.discovery.include}
          onChange={v => setNested("discovery", "include", v)}
          disabled={excludeActive}
          placeholder="COM3"
          note={excludeActive ? "Include and Exclude can't be used together." : undefined}
          helpComponent={
            <Help
              title="Include"
              what="Probe only these COM ports during discovery."
              defaultVal="Empty (probe every enumerated port)."
              when="Cannot be combined with Exclude."
            />
          }
        />
        <ListField label="Exclude"
          values={form.discovery.exclude}
          onChange={v => setNested("discovery", "exclude", v)}
          disabled={includeActive}
          placeholder="COM7"
          note={includeActive ? "Include and Exclude can't be used together." : undefined}
          helpComponent={
            <Help
              title="Exclude"
              what="Skip these COM ports during discovery."
              defaultVal="Empty."
              when="Cannot be combined with Include."
            />
          }
        />
        <Field label="Post-open settle"
          helpComponent={
            <Help
              title="Post-open settle (ms)"
              what="Wait period after opening a serial port before probing it. Covers the Arduino bootloader reset window."
              defaultVal="2000."
            />
          }>
          <div className="shp-input-row" style={{ maxWidth: 220 }}>
            <input className="shp-input shp-input--mono" type="number" min={0}
              value={form.discovery.post_open_settle_ms}
              onChange={e => setNested("discovery", "post_open_settle_ms", Number(e.target.value) || 0)} />
            <span className="shp-muted">ms</span>
          </div>
        </Field>
      </Section>

      <Section
        title="Log"
        helpComponent={<Help title="Log" what="Verbosity of the service's structured log." />}
      >
        <Field label="Level"
          helpComponent={
            <Help
              title="Level"
              what="Logging verbosity for the service log."
              defaultVal="info."
              when="Increase to debug when triaging a problem; warn or error in production if logs are too noisy."
            />
          }>
          <select className="shp-select" style={{ width: 160 }}
            value={form.log.level}
            onChange={e => setNested("log", "level", e.target.value)}>
            <option>debug</option><option>info</option><option>warn</option><option>error</option>
          </select>
        </Field>
      </Section>

      <Section title="Raw serial"
        helpComponent={
          <Help
            title="Raw serial"
            what="Exposes GET /serial/ports + POST /serial/ports/{port}/command for diagnostics. Bypasses device classification."
            defaultVal="off."
            when="Enable only when actively probing the wire."
          />
        }>
        <Field label="Enabled">
          <Checkbox label="Allow raw passthrough on discovered ports"
            checked={form.raw_serial.enabled}
            onChange={v => setNested("raw_serial", "enabled", v)} />
        </Field>
      </Section>

      <Section title="Auto-update"
        helpComponent={
          <Help
            title="Auto-update"
            what="When on, the panel checks GitHub Releases on launch and every 6 hours, then offers to download and install new SerialHop versions."
            defaultVal="on."
          />
        }>
        <Field label="Enabled">
          <Checkbox label="Check for updates automatically"
            checked={form.auto_update.enabled}
            onChange={v => setNested("auto_update", "enabled", v)} />
        </Field>
      </Section>

      <Section title="Firmware flashing"
        helpComponent={
          <Help
            title="Firmware flashing"
            what="Allows the service to flash firmware to detected boards."
            defaultVal="off."
          />
        }>
        <div className="shp-info-block">
          <span className="shp-info-block__icon">&#x26A0;</span>
          <span>
            Firmware flashing is higher risk than raw serial — a bad .hex bricks
            the board (ISP recovery required). Leave disabled unless you&apos;re
            actively flashing devices.
          </span>
        </div>
        <Field label="Enabled"
          helpComponent={
            <Help
              title="Enabled"
              what="Allows the service to flash firmware to detected boards."
              defaultVal="off."
            />
          }>
          <Checkbox label="Allow firmware flashing through the service"
            checked={form.flashing.enabled}
            onChange={v => setNested("flashing", "enabled", v)} />
        </Field>
        <Field label="Backup directory" disabled={flashOff}
          helpComponent={
            <Help
              title="Backup directory"
              what="Where the service writes a backup of a board's existing firmware before flashing it."
              defaultVal="%ProgramData%\\SerialHop\\backups."
            />
          }>
          <div className="shp-input-row">
            <input className="shp-input shp-input--mono"
              value={form.flashing.backup_dir}
              disabled={flashOff}
              data-error={!!errFor(errors, "flashing.backup_dir") || undefined}
              onChange={e => setNested("flashing", "backup_dir", e.target.value)} />
            <Button small disabled={flashOff}
              onClick={async () => { const d = await PickBackupDir(); if (d) setNested("flashing", "backup_dir", d); }}>
              Choose…
            </Button>
          </div>
          {errFor(errors, "flashing.backup_dir") && <div className="shp-error">{errFor(errors, "flashing.backup_dir")}</div>}
        </Field>
        <Field label="Keep N backups" disabled={flashOff}
          helpComponent={
            <Help
              title="Keep N backups"
              what="How many per-board backup files to retain. Oldest beyond this count are deleted."
              defaultVal="10."
              when="0 keeps all backups indefinitely."
            />
          }>
          <input className="shp-input shp-input--mono" type="number" min={0}
            value={form.flashing.keep_n}
            disabled={flashOff}
            style={{ maxWidth: 100 }}
            onChange={e => setNested("flashing", "keep_n", Number(e.target.value) || 0)} />
        </Field>
      </Section>

      <div
        className="shp-btn-row"
        style={{ marginTop: 14, paddingTop: 4, borderTop: "1px solid var(--border)" }}
      >
        <Button variant="primary" disabled={!dirty} onClick={() => save(false)}>Save</Button>
        <Button variant="primary" elevated disabled={!dirty} onClick={() => save(true)}>Save &amp; restart</Button>
        <Button disabled={!dirty} onClick={discard}>Discard changes</Button>
        <span className="shp-gap" />
        <Button variant="ghost" onClick={() => OpenConfigInEditor()}>Open in editor ↗</Button>
      </div>

      {pendingConfirm !== null && (
        <Modal
          title="Couldn't reach the lab-bridge"
          sub="Save credentials without verifying?"
          actions={
            <>
              <Button variant="ghost" onClick={() => setPendingConfirm(null)}>Cancel</Button>
              <Button variant="primary" onClick={async () => { const r = pendingConfirm.alsoRestart; setPendingConfirm(null); await doSave(r); }}>
                Save anyway
              </Button>
            </>
          }
        >
          <p>
            The panel couldn&apos;t reach <code>{form.lab_bridge.host}</code> to verify the new credentials
            {pendingConfirm.detail ? <> (<code>{pendingConfirm.detail}</code>)</> : null}.
          </p>
          <p>
            You can save them anyway and the service will retry on its next start. If the credentials
            are wrong, the Tunnel lamp will turn red once the server comes back.
          </p>
        </Modal>
      )}
    </>
  );
});

function errFor(errs: FieldErrorDTO[], field: string): string | undefined {
  return errs.find(e => e.field === field)?.detail;
}

// Human labels for diff'd fields surfaced in the unsaved-changes modal.
// Order matches the on-screen rendering so the list reads top-to-bottom.
const FIELD_LABELS: { path: (c: ConfigDTO) => unknown; label: string }[] = [
  { path: c => c.lab_bridge.host, label: "Host" },
  { path: c => c.lab_bridge.user, label: "Username" },
  { path: c => c.lab_bridge.pass, label: "Password" },
  { path: c => c.rest.port, label: "REST port" },
  { path: c => c.discovery.include.join(","), label: "Include" },
  { path: c => c.discovery.exclude.join(","), label: "Exclude" },
  { path: c => c.discovery.post_open_settle_ms, label: "Post-open settle" },
  { path: c => c.log.level, label: "Log level" },
  { path: c => c.raw_serial.enabled, label: "Raw serial" },
  { path: c => c.auto_update.enabled, label: "Auto-update" },
  { path: c => c.flashing.enabled, label: "Firmware flashing" },
  { path: c => c.flashing.backup_dir, label: "Backup directory" },
  { path: c => c.flashing.keep_n, label: "Keep N backups" },
];

function diffFieldLabels(a: ConfigDTO, b: ConfigDTO): string[] {
  return FIELD_LABELS.filter(f => f.path(a) !== f.path(b)).map(f => f.label);
}

interface ListFieldProps {
  label: string;
  values: string[];
  onChange: (v: string[]) => void;
  disabled?: boolean;
  note?: string;
  placeholder?: string;
  helpComponent?: React.ReactNode;
}

function ListField({ label, values, onChange, disabled, note, placeholder, helpComponent }: ListFieldProps) {
  return (
    <Field label={label} disabled={disabled} helpComponent={helpComponent}>
      <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
        {values.map((v, i) => (
          <div key={i} className="shp-listrow">
            <input className="shp-input shp-input--mono"
              value={v} disabled={disabled} placeholder={placeholder}
              style={{ maxWidth: 200 }}
              onChange={e => { const copy = [...values]; copy[i] = e.target.value; onChange(copy); }} />
            <Button small variant="ghost" disabled={disabled}
              onClick={() => onChange(values.filter((_, j) => j !== i))}>Remove</Button>
          </div>
        ))}
        <Button small variant="ghost" disabled={disabled}
          style={{ alignSelf: "flex-start" }}
          onClick={() => onChange([...values, ""])}>+ Add row</Button>
        {note && <div className="shp-disabled-note">{note}</div>}
      </div>
    </Field>
  );
}
