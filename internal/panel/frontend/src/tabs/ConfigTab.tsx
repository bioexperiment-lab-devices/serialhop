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
  }), [form, discard]);

  if (!form || !loaded) return <div>Loading…</div>;

  const setNested = <S extends keyof ConfigDTO, K extends keyof ConfigDTO[S]>(
    sec: S, key: K, v: ConfigDTO[S][K],
  ) => setForm(prev => prev && { ...prev, [sec]: { ...(prev[sec] as object), [key]: v } } as ConfigDTO);

  const credsMissing = !form.lab_bridge.user || !form.lab_bridge.pass;

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
    <div className="config-tab">
      {credsMissing && (
        <div className="shp-banner" data-tone="info">
          Enter your lab-bridge credentials to enable the service.
        </div>
      )}

      <Section title="Lab-bridge">
        <Field label="Host" helpComponent={<Help title="Host" what="lab-bridge VPS host." defaultVal="111.88.145.138" />}>
          <input value={form.lab_bridge.host} onChange={e => setNested("lab_bridge", "host", e.target.value)} />
        </Field>
        <Field label="Username" hint={errFor(errors, "lab_bridge.user")}
          helpComponent={
            <Help
              title="Username"
              what="The lab-bridge account username that authenticates this client's chisel tunnel and REST calls."
              when="Save will verify these credentials against the lab-bridge."
            />
          }>
          <input value={form.lab_bridge.user} onChange={e => setNested("lab_bridge", "user", e.target.value)} />
        </Field>
        <Field label="Password" hint={errFor(errors, "lab_bridge.pass")}
          helpComponent={
            <Help
              title="Password"
              what="The lab-bridge account password. Stored as plaintext in the YAML (matches the existing convention)."
              when="Save will verify these credentials against the lab-bridge."
            />
          }>
          <input value={form.lab_bridge.pass} onChange={e => setNested("lab_bridge", "pass", e.target.value)} />
        </Field>
      </Section>

      <Section title="REST">
        <Field label="Port" helpComponent={<Help title="REST port" what="Local TCP port the SerialHop service binds." defaultVal="0 (OS-assigned)" />}>
          <input type="number" min={0} max={65535} value={form.rest.port}
            onChange={e => setNested("rest", "port", Number(e.target.value) || 0)} />
        </Field>
      </Section>

      <Section title="Discovery">
        <ListField label="Include"
          values={form.discovery.include}
          onChange={v => setNested("discovery", "include", v)}
          disabled={form.discovery.exclude.length > 0}
          note={form.discovery.exclude.length > 0 ? "Include and Exclude can't be used together" : undefined}
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
          disabled={form.discovery.include.length > 0}
          note={form.discovery.include.length > 0 ? "Include and Exclude can't be used together" : undefined}
          helpComponent={
            <Help
              title="Exclude"
              what="Skip these COM ports during discovery."
              defaultVal="Empty."
              when="Cannot be combined with Include."
            />
          }
        />
        <Field label="Post-open settle (ms)"
          helpComponent={
            <Help
              title="Post-open settle (ms)"
              what="Wait period after opening a serial port before probing it. Covers the Arduino bootloader reset window."
              defaultVal="2000."
            />
          }>
          <input type="number" min={0} value={form.discovery.post_open_settle_ms}
            onChange={e => setNested("discovery", "post_open_settle_ms", Number(e.target.value) || 0)} />
        </Field>
      </Section>

      <Section title="Log">
        <Field label="Level"
          helpComponent={
            <Help
              title="Level"
              what="Logging verbosity for the service log."
              defaultVal="info."
              when="Increase to debug when triaging a problem; warn or error in production if logs are too noisy."
            />
          }>
          <select value={form.log.level} onChange={e => setNested("log", "level", e.target.value)}>
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
        <Field label="">
          <Checkbox label="Enabled" checked={form.raw_serial.enabled}
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
        <Field label="">
          <Checkbox label="Enabled" checked={form.auto_update.enabled}
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
        <p className="shp-section__info">
          Firmware flashing is higher risk than raw serial — a bad .hex bricks
          the board (ISP recovery required). Leave disabled unless you&apos;re
          actively flashing devices.
        </p>
        <Field label="Enabled"
          helpComponent={
            <Help
              title="Enabled"
              what="Allows the service to flash firmware to detected boards."
              defaultVal="off."
            />
          }>
          <Checkbox label="" checked={form.flashing.enabled}
            onChange={v => setNested("flashing", "enabled", v)} />
        </Field>
        <Field label="Backup directory" disabled={!form.flashing.enabled}
          helpComponent={
            <Help
              title="Backup directory"
              what="Where the service writes a backup of a board's existing firmware before flashing it."
              defaultVal="%ProgramData%\\SerialHop\\backups."
            />
          }>
          <input value={form.flashing.backup_dir}
            disabled={!form.flashing.enabled}
            onChange={e => setNested("flashing", "backup_dir", e.target.value)} />
          <Button small disabled={!form.flashing.enabled}
            onClick={async () => { const d = await PickBackupDir(); if (d) setNested("flashing", "backup_dir", d); }}>
            Pick…
          </Button>
        </Field>
        <Field label="Keep N backups" disabled={!form.flashing.enabled}
          helpComponent={
            <Help
              title="Keep N backups"
              what="How many per-board backup files to retain. Oldest beyond this count are deleted."
              defaultVal="10."
              when="0 keeps all backups indefinitely."
            />
          }>
          <input type="number" min={0} value={form.flashing.keep_n}
            disabled={!form.flashing.enabled}
            onChange={e => setNested("flashing", "keep_n", Number(e.target.value) || 0)} />
        </Field>
      </Section>

      <div className="config-actions">
        <Button variant="primary" disabled={!dirty} onClick={() => save(false)}>Save</Button>
        <Button variant="primary" elevated disabled={!dirty} onClick={() => save(true)}>Save &amp; restart</Button>
        <Button variant="ghost" disabled={!dirty} onClick={discard}>Discard changes</Button>
        <Button variant="ghost" onClick={() => OpenConfigInEditor()}>Open in editor</Button>
      </div>

      {pendingConfirm !== null && (
        <Modal
          title="Couldn't verify credentials"
          actions={
            <>
              <Button variant="ghost" onClick={() => setPendingConfirm(null)}>Cancel</Button>
              <Button variant="primary" onClick={async () => { const r = pendingConfirm.alsoRestart; setPendingConfirm(null); await doSave(r); }}>
                Save anyway
              </Button>
            </>
          }
        >
          <p>Couldn&apos;t reach the server to verify the credentials ({pendingConfirm.detail}). Save anyway?</p>
        </Modal>
      )}
    </div>
  );
});

function errFor(errs: FieldErrorDTO[], field: string): string | undefined {
  return errs.find(e => e.field === field)?.detail;
}

interface ListFieldProps {
  label: string;
  values: string[];
  onChange: (v: string[]) => void;
  disabled?: boolean;
  note?: string;
  helpComponent?: React.ReactNode;
}

function ListField({ label, values, onChange, disabled, note, helpComponent }: ListFieldProps) {
  return (
    <Field label={label} hint={note} disabled={disabled} helpComponent={helpComponent}>
      <div className="list-field">
        {values.map((v, i) => (
          <div key={i} className="list-field__row">
            <input value={v} disabled={disabled}
              onChange={e => { const copy = [...values]; copy[i] = e.target.value; onChange(copy); }} />
            <Button small disabled={disabled}
              onClick={() => onChange(values.filter((_, j) => j !== i))}>Remove</Button>
          </div>
        ))}
        <Button small disabled={disabled} onClick={() => onChange([...values, ""])}>Add row</Button>
      </div>
    </Field>
  );
}
