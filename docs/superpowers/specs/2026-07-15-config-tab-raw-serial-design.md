# Restore `raw_serial` to the Config tab — design

**Date:** 2026-07-15
**Status:** accepted

## Problem

PR #192 ("raw serial port attach over websocket") brought the most-direct COM-port
access back to SerialHop v2 and added a `raw_serial` config block:

```yaml
raw_serial:
  enabled: false
  idle_timeout_ms: 900000
```

The block is present in `config.Config`, in the generated scaffold, and is honored by
the service. **But the panel's Config tab does not surface it.** The Wails-rewritten
UI (PR #68) only renders Lab-bridge / REST / Discovery / Log / Auto-update / Firmware
flashing. An operator cannot see or toggle raw serial access from the panel — the only
way to enable it is to hand-edit the YAML via "Open in editor".

This is a regression relative to intent: every other service-gating flag (`flashing`,
`auto_update`) has a first-class control in the Config tab; `raw_serial` should too.

## Two questions

### 1. Does the v0/v1 `raw_serial` setting fit the v2 feature?

Yes — cleanly, and upgrades are seamless.

- v0/v1 (`#44`, removed in `#184`) used the same YAML key and field:
  ```yaml
  raw_serial:
    enabled: false
  ```
- v2 (`#192`) reuses the exact same key/field and adds `idle_timeout_ms`.
- `config.Load` starts from `Default()` and unmarshals the file **over** it
  (`load.go`). YAML-v3 only overwrites fields present in the document, so an old
  config carrying only `raw_serial.enabled: true` upgrades to
  `{enabled: true, idle_timeout_ms: 900000}` — the missing key inherits the default.

Net effect: a client upgraded from v0/v1 with raw serial **on** keeps raw serial on
(now backed by the new WebSocket attach endpoint) and inherits a sane 15-minute idle
timeout, without touching their config. A client with it **off** stays off. Nobody
has to notice the format change.

### 2. What must change to bring the setting back?

Only the panel surface and a small server-side bounds check. The config struct,
scaffold, and service wiring already exist.

## Design

### Panel Config tab (`ConfigTab.tsx`)

Add a "Raw serial access" `Section` mirroring the existing "Firmware flashing"
section:

- An **Enabled** checkbox bound to `raw_serial.enabled`.
- An **Idle timeout (ms)** integer input bound to `raw_serial.idle_timeout_ms`,
  disabled while raw serial is off (same pattern as flashing's backup-dir being
  disabled when flashing is off). Hint text: `0 = never time out`.
- A warning info-block noting this exposes a raw byte + line-control stream on
  undiscovered ports; leave off unless doing bring-up / reverse-engineering.

Supporting edits:

- Extend the local `ConfigDTO` interface with
  `raw_serial: { enabled: boolean; idle_timeout_ms: number }`.
- Add `Raw serial access` / `Raw serial idle timeout` rows to `FIELD_LABELS` so the
  unsaved-changes modal reports these fields by name.

No change is needed to the save/load transport: `App.ts` binds
`LoadConfigFromDisk`/`SaveConfig` as `any` pass-throughs, so `raw_serial` already
round-trips at runtime; this change only makes it visible and editable.

### Vendored Wails bindings

`models.ts` and `App.d.ts` are committed placeholders (see `src/wails/README.md`).
Update them to include `RawSerialConfig` so the committed shapes match what
`wails build` will regenerate. `App.ts` stays `any` and is unchanged.

### Server-side validation (`config/load.go`)

Add a bounds check so the panel can't persist a nonsensical value:

```
if c.RawSerial.IdleTimeoutMs < 0 {
    return fmt.Errorf("raw_serial.idle_timeout_ms must be >= 0 (got %d)", ...)
}
```

`0` remains valid (means "never time out", matching the scaffold comment). This
closes the follow-up noted in #192's description.

## Out of scope

- No change to the attach endpoint, wire protocol, registry lease, or Python client.
- No change to the scaffold template (already correct since #192).
- No new config fields.

## Testing

- Go: unit test proving a v0-style config (`raw_serial.enabled: true`, no
  `idle_timeout_ms`) loads as `{true, 900000}`; unit test for the new negative
  `idle_timeout_ms` validation error.
- Frontend: vitest coverage that the raw-serial checkbox toggles dirty state, the
  idle-timeout input enables/disables with it, and the values survive a save
  round-trip (`SaveConfig` receives the edited `raw_serial`).
