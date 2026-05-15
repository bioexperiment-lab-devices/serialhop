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
import { EventsEmit } from "../wails/runtime/runtime";
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

const CRED_REJECTED_MSG = "Server rejected these credentials. Check the username and password.";

// Strip blank rows from include/exclude before validation/save so an
// operator who clicked "+ Add row" but never filled it in doesn't ship
// empty strings into config.yaml.
function cleanForSave(cfg: ConfigDTO): ConfigDTO {
  const trim = (xs: string[]) => xs.map(s => s.trim()).filter(s => s !== "");
  return {
    ...cfg,
    discovery: {
      ...cfg.discovery,
      include: trim(cfg.discovery.include),
      exclude: trim(cfg.discovery.exclude),
    },
  };
}

// Lightweight host check used for the on-blur inline validator. Accepts
// IPv4 dotted-quad (each octet 0-255) or an RFC1123-ish hostname.
// Server-side ValidateConfig is still authoritative on save.
function validateHostInput(s: string): string | null {
  const v = s.trim();
  if (!v) return "Host is required.";
  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(v)) {
    const ok = v.split(".").every(n => { const x = Number(n); return x >= 0 && x <= 255; });
    return ok ? null : "Must be a hostname or IPv4 address.";
  }
  const HOST_RE = /^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$/;
  return HOST_RE.test(v) ? null : "Must be a hostname or IPv4 address.";
}

function scrollToField(field: string) {
  // Defer to next frame so React has actually rendered the error decoration
  // (data-error attributes, .shp-error nodes) before we measure positions.
  requestAnimationFrame(() => {
    const node = document.querySelector(`[data-field="${field}"]`) as HTMLElement | null;
    if (!node) return;
    // jsdom doesn't implement scrollIntoView; guard so unit tests don't
    // throw during the deferred rAF callback.
    if (typeof node.scrollIntoView === "function") {
      node.scrollIntoView({ behavior: "smooth", block: "center" });
    }
    const input = node.querySelector("input, select, textarea") as HTMLElement | null;
    if (input) input.focus();
  });
}

// Pick the field to scroll to: explicit override wins, otherwise the
// first error in the list. Returns null when there's nothing to do.
function firstErrorField(errs: FieldErrorDTO[]): string | null {
  return errs.length > 0 ? errs[0].field : null;
}

export const ConfigTab = forwardRef<ConfigTabHandle, Props>(function ConfigTab({ onDirtyChange }, ref) {
  const [loaded, setLoaded] = useState<ConfigDTO | null>(null);
  const [form, setForm] = useState<ConfigDTO | null>(null);
  const [errors, setErrors] = useState<FieldErrorDTO[]>([]);
  // hostError is the client-side, on-blur validation of the Host input.
  // Merged with `errors` at render time so the host row decorates the
  // same way whether the error came from local validation or the backend.
  const [hostError, setHostError] = useState<string | null>(null);
  const [pendingConfirm, setPendingConfirm] = useState<{ detail: string; alsoRestart: boolean } | null>(null);

  useEffect(() => {
    LoadConfigFromDisk().then((cfg: ConfigDTO) => { setLoaded(clone(cfg)); setForm(clone(cfg)); });
  }, []);

  const dirty = !!(loaded && form && JSON.stringify(loaded) !== JSON.stringify(form));
  useEffect(() => { onDirtyChange(dirty); }, [dirty, onDirtyChange]);

  const discard = () => {
    if (loaded) { setForm(clone(loaded)); setErrors([]); setHostError(null); }
  };

  // Effective error list seen by the renderer: backend errors plus the
  // inline host error (de-duped — backend error wins if both exist for
  // the same field).
  const renderErrors: FieldErrorDTO[] = (() => {
    if (!hostError) return errors;
    if (errors.some(e => e.field === "lab_bridge.host")) return errors;
    return [{ field: "lab_bridge.host", detail: hostError }, ...errors];
  })();

  useImperativeHandle(ref, () => ({
    save: async () => {
      if (!form) return false;
      const payload = cleanForSave(form);
      const vErrs = await ValidateConfig(payload);
      if (vErrs && vErrs.length) {
        setErrors(vErrs);
        const f = firstErrorField(vErrs);
        if (f) scrollToField(f);
        return false;
      }
      setErrors([]);
      const verify = await VerifyCredentials(payload.lab_bridge.host, payload.lab_bridge.user, payload.lab_bridge.pass);
      if (verify.outcome === "unauthorized") {
        setErrors([
          { field: "lab_bridge.user", detail: CRED_REJECTED_MSG },
          { field: "lab_bridge.pass", detail: CRED_REJECTED_MSG },
        ]);
        scrollToField("lab_bridge.user");
        return false;
      }
      if (verify.outcome === "needs_confirm") {
        // Operator must resolve the confirm modal first — save cannot complete now.
        setPendingConfirm({ detail: verify.detail || "", alsoRestart: false });
        return false;
      }
      const res = await SaveConfig(payload);
      if (!res.ok) {
        setErrors(res.field_errors || []);
        const f = firstErrorField(res.field_errors || []);
        if (f) scrollToField(f);
        return false;
      }
      setLoaded(clone(payload));
      setForm(clone(payload));
      // Imperative save() is the path used by the unsaved-guard modal
      // when the operator picks "Save" while switching tabs — same
      // restart-required reminder applies.
      EventsEmit("footer:set", {
        kind: "ok",
        text: "<b>Saved.</b> Restart the service to apply.",
        time: new Date().toISOString(),
      });
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

  // Local form-level errFor uses `renderErrors` so that the on-blur
  // host validator decorates the row even before a Save is attempted.
  const e = (field: string) => renderErrors.find(x => x.field === field)?.detail;

  const save = async (alsoRestart: boolean) => {
    const payload = cleanForSave(form);
    const vErrs = await ValidateConfig(payload);
    if (vErrs && vErrs.length) {
      setErrors(vErrs);
      const f = firstErrorField(vErrs);
      if (f) scrollToField(f);
      return;
    }
    setErrors([]);
    const verify = await VerifyCredentials(payload.lab_bridge.host, payload.lab_bridge.user, payload.lab_bridge.pass);
    if (verify.outcome === "unauthorized") {
      setErrors([
        { field: "lab_bridge.user", detail: CRED_REJECTED_MSG },
        { field: "lab_bridge.pass", detail: CRED_REJECTED_MSG },
      ]);
      scrollToField("lab_bridge.user");
      return;
    }
    if (verify.outcome === "needs_confirm") {
      setPendingConfirm({ detail: verify.detail || "", alsoRestart });
      return;
    }
    await doSave(alsoRestart);
  };

  const doSave = async (alsoRestart: boolean) => {
    const payload = cleanForSave(form);
    const res = await SaveConfig(payload);
    if (!res.ok) {
      setErrors(res.field_errors || []);
      const f = firstErrorField(res.field_errors || []);
      if (f) scrollToField(f);
      return;
    }
    setLoaded(clone(payload));
    setForm(clone(payload));
    if (alsoRestart) {
      // The elevated RestartService action emits its own footer states
      // ("Working…", "Service restarted", failure), so don't pre-empt them.
      await RestartService();
      return;
    }
    // Save-only path: the new YAML is on disk but the running service is
    // still using the previous config. Surface a footer reminder so the
    // operator knows a restart is needed for the change to take effect.
    EventsEmit("footer:set", {
      kind: "ok",
      text: "<b>Saved.</b> Restart the service to apply.",
      time: new Date().toISOString(),
    });
  };

  const onHostBlur = (v: string) => {
    setHostError(validateHostInput(v));
  };
  // When the user starts editing the host again, clear backend host errors
  // so the row doesn't stay red while the operator types a fix.
  const onHostChange = (v: string) => {
    setNested("lab_bridge", "host", v);
    if (errors.some(x => x.field === "lab_bridge.host")) {
      setErrors(errors.filter(x => x.field !== "lab_bridge.host"));
    }
    if (hostError) setHostError(null);
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
        <Field label="Host" dataField="lab_bridge.host"
          helpComponent={<Help title="Host" what="lab-bridge VPS host." defaultVal="111.88.145.138" />}>
          <input className="shp-input shp-input--mono"
            value={form.lab_bridge.host}
            data-error={!!e("lab_bridge.host") || undefined}
            onChange={ev => onHostChange(ev.target.value)}
            onBlur={ev => onHostBlur(ev.target.value)} />
          {e("lab_bridge.host") && <div className="shp-error">{e("lab_bridge.host")}</div>}
        </Field>
        <Field label="Username" dataField="lab_bridge.user"
          helpComponent={
            <Help
              title="Username"
              what="The lab-bridge account username that authenticates this client's chisel tunnel and REST calls."
              when="Save will verify these credentials against the lab-bridge."
            />
          }>
          <input className="shp-input"
            value={form.lab_bridge.user}
            data-error={!!e("lab_bridge.user") || undefined}
            onChange={ev => setNested("lab_bridge", "user", ev.target.value)} />
          {e("lab_bridge.user") && <div className="shp-error">{e("lab_bridge.user")}</div>}
        </Field>
        <Field label="Password" dataField="lab_bridge.pass"
          helpComponent={
            <Help
              title="Password"
              what="The lab-bridge account password. Stored as plaintext in the YAML (matches the existing convention)."
              when="Save will verify these credentials against the lab-bridge."
            />
          }>
          <input className="shp-input shp-input--mono"
            value={form.lab_bridge.pass}
            data-error={!!e("lab_bridge.pass") || undefined}
            onChange={ev => setNested("lab_bridge", "pass", ev.target.value)} />
          {e("lab_bridge.pass") && <div className="shp-error">{e("lab_bridge.pass")}</div>}
        </Field>
      </Section>

      <Section
        title="REST"
        helpComponent={<Help title="REST" what="The local HTTP server other lab tools on this machine talk to." />}
      >
        <Field label="Port" dataField="rest.port"
          helpComponent={<Help title="REST port" what="Local TCP port the SerialHop service binds." defaultVal="0 (OS-assigned)" />}>
          <input className="shp-input shp-input--mono" type="number" min={0} max={65535}
            value={form.rest.port}
            style={{ maxWidth: 120 }}
            onChange={ev => setNested("rest", "port", Number(ev.target.value) || 0)} />
          <div className="shp-field__hint">0 = OS picks a free port</div>
        </Field>
      </Section>

      <Section
        title="Discovery"
        helpComponent={<Help title="Discovery" what="How the service searches the local serial ports for known device types." />}
      >
        <ListField label="Include" dataField="discovery.include"
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
        <ListField label="Exclude" dataField="discovery.exclude"
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
        <Field label="Post-open settle" dataField="discovery.post_open_settle_ms"
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
              onChange={ev => setNested("discovery", "post_open_settle_ms", Number(ev.target.value) || 0)} />
            <span className="shp-muted">ms</span>
          </div>
        </Field>
      </Section>

      <Section
        title="Log"
        helpComponent={<Help title="Log" what="Verbosity of the service's structured log." />}
      >
        <Field label="Level" dataField="log.level"
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
            onChange={ev => setNested("log", "level", ev.target.value)}>
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
        <Field label="Enabled" dataField="raw_serial.enabled">
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
        <Field label="Enabled" dataField="auto_update.enabled">
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
        <Field label="Enabled" dataField="flashing.enabled"
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
        <Field label="Backup directory" dataField="flashing.backup_dir" disabled={flashOff}
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
              data-error={!!e("flashing.backup_dir") || undefined}
              onChange={ev => setNested("flashing", "backup_dir", ev.target.value)} />
            <Button small disabled={flashOff}
              onClick={async () => { const d = await PickBackupDir(); if (d) setNested("flashing", "backup_dir", d); }}>
              Choose…
            </Button>
          </div>
          {e("flashing.backup_dir") && <div className="shp-error">{e("flashing.backup_dir")}</div>}
        </Field>
        <Field label="Keep N backups" dataField="flashing.keep_n" disabled={flashOff}
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
            onChange={ev => setNested("flashing", "keep_n", Number(ev.target.value) || 0)} />
        </Field>
      </Section>

      <div
        className="shp-btn-row"
        style={{ marginTop: 14, paddingTop: 4, borderTop: "1px solid var(--border)" }}
      >
        <Button variant="primary" elevated disabled={!dirty} onClick={() => save(true)}>Save &amp; restart</Button>
        <Button variant="primary" disabled={!dirty} onClick={() => save(false)}>Save</Button>
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
  dataField?: string;
  helpComponent?: React.ReactNode;
}

function ListField({ label, values, onChange, disabled, note, placeholder, dataField, helpComponent }: ListFieldProps) {
  return (
    <Field label={label} disabled={disabled} dataField={dataField} helpComponent={helpComponent}>
      <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
        {values.map((v, i) => (
          <div key={i} className="shp-listrow">
            <input className="shp-input shp-input--mono"
              value={v} disabled={disabled} placeholder={placeholder}
              style={{ maxWidth: 200 }}
              onChange={ev => { const copy = [...values]; copy[i] = ev.target.value; onChange(copy); }} />
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
