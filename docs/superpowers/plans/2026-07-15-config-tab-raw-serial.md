# Restore `raw_serial` to the Config tab — implementation plan

**Design:** `docs/superpowers/specs/2026-07-15-config-tab-raw-serial-design.md`
**Date:** 2026-07-15

One logical change → one PR, title: `feat(panel): restore raw serial settings to the Config tab`.

## Task 1 — server-side validation (Go)

- `internal/config/load.go`: in `Validate`, reject `raw_serial.idle_timeout_ms < 0`.
- `internal/config/load_test.go`: add
  - `TestLoad_V0RawSerialUpgrades` — write a YAML with only `raw_serial.enabled: true`
    (plus required lab_bridge creds), assert loaded `RawSerial == {true, 900000}`.
  - `TestValidate_NegativeIdleTimeout` — assert `Validate` errors on -1.
- Run `go test ./internal/config/...`.

## Task 2 — panel Config tab (TS)

- `internal/panel/frontend/src/tabs/ConfigTab.tsx`:
  - Add `raw_serial: { enabled: boolean; idle_timeout_ms: number }` to `ConfigDTO`.
  - Add a "Raw serial access" `<Section>` after "Firmware flashing": warning block,
    Enabled `<Checkbox>`, and an idle-timeout `<IntegerInput>` (disabled when off,
    min 0) with a `0 = never time out` hint.
  - Add `FIELD_LABELS` rows for `raw_serial.enabled` and `raw_serial.idle_timeout_ms`.
- `internal/panel/frontend/src/tabs/ConfigTab.test.tsx`:
  - Add `raw_serial` to `seedCfg`.
  - Test: toggling Enabled marks dirty; idle-timeout input is disabled when off and
    enabled when on; `SaveConfig` payload carries the edited `raw_serial`.

## Task 3 — vendored bindings + preview parity (TS)

- `internal/panel/frontend/src/wails/wailsjs/go/models.ts`: add `RawSerialConfig`
  class and a `raw_serial` field on `Config`.
- `internal/panel/frontend/src/preview-shim/*` and any other `ConfigDTO` seed
  (`seed.ts`, `bindings.ts`): add `raw_serial` so the preview harness renders.

## Task 4 — verify

- `gofmt -l .`, `go vet ./...`, `golangci-lint run`, `go test -race -count=1 ./...`,
  `govulncheck ./...` (or `task test`).
- Frontend: `npm --prefix internal/panel/frontend run test` and
  `npm --prefix internal/panel/frontend run build` (tsc --noEmit).
- Manually launch the panel preview to eyeball the new section.

## Task 5 — PR

- Push, open PR with the conventional title above, wait for CI green, squash-merge.
