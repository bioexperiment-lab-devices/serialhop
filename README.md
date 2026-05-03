<p align="center">
  <img src="assets/serialhop.webp" alt="SerialHop" width="320">
</p>

# SerialHop

Single-binary Go application that exposes serial-port lab devices to a remote HTTP client through a chisel reverse tunnel. Runs as a Windows service; managed through a small native control-panel window. Streams its own logs back to the central Loki/Grafana stack over the same chisel session.

## Build

Default target is Windows / amd64:
```
task build
```

Override target via env variables:
```
task build GOOS=linux GOARCH=arm64
```

Output: `dist/SerialHop.exe`.

The build embeds an icon, a UAC manifest (`asInvoker`), and version metadata via `goversioninfo`. The first build downloads `goversioninfo` automatically. `assets/manifest.xml` is generated at build time from `assets/manifest.template.xml` and the version in `assets/version.json` (the generated file is gitignored). The version baked into the binary via `-ldflags -X` is `<base>+<git-describe>` — e.g. `0.3.0+v0.3.0` on a clean release, `0.3.0+v0.3.0-7-gabc1234-dirty` on a working-tree dev build.

`jq` is required on the build host. Preinstalled on all GitHub-hosted runners; `brew install jq` on macOS or `apt-get install jq` on Debian/Ubuntu.

## Releases

Releases are managed by [release-please](https://github.com/googleapis/release-please). The flow is fully hands-off:

1. Merge a `feat:` / `fix:` PR to `main`.
2. release-please opens (or updates) a release PR titled `chore(main): release X.Y.Z`. It's authored by a dedicated GitHub App, so `pr.yml` checks fire automatically.
3. Watch `verify` go green on the release PR.
4. Click **Squash and merge** on the release PR.
5. The `release-build` job runs on `windows-latest` and publishes a GitHub Release with `SerialHop-vX.Y.Z.exe`, `SHA256SUMS.txt`, and a Sigstore build-provenance attestation.

There is no scheduled cadence — releases ship only when the release PR is merged by hand.

Verify a downloaded binary:
```
gh release download vX.Y.Z -p "SerialHop-*.exe" -p "SHA256SUMS.txt"
shasum -a 256 -c SHA256SUMS.txt
gh attestation verify SerialHop-vX.Y.Z.exe --owner bioexperiment-lab-devices
```

## Install on a Windows lab machine

1. Copy `SerialHop.exe` to an install location (e.g., `C:\Tools\SerialHop\`).
2. Double-click the .exe. The control panel opens. On first launch it writes `SerialHop_config.yaml` next to the .exe and shows a validation warning if anything's wrong.
3. Click **Open config file**, set `chisel.remote_port` and `chisel.user`/`chisel.pass` (and any other site-specific values), save.
4. Click **Install**. UAC prompts; approve. The service is registered as `SerialHop` (auto-start at boot, runs as LocalSystem) and started immediately.

After install:

- The service runs across reboots without the panel being open.
- To apply config changes: edit the YAML file, then click **Restart** in the panel.
- To remove: click **Uninstall** in the panel.
- Logs go to `SerialHop.log` (slog JSON) and `SerialHop_stderr.log` (chisel state, panic traces) next to the .exe — both rotated at 10 MB with 3 backups. Click **Open log file** to view the main log.

## Windows Defender false positives

The combination this binary uses — Go static link, `-H windowsgui`, embedded chisel reverse tunnel, Windows service worker — matches the heuristic shape of a RAT closely enough that Defender (and some other AV engines) sometimes quarantines the `.exe` on download or first run. Build flags already mitigate the most common triggers (`-trimpath`, no symbol stripping, populated version metadata), but unsigned binaries hit zero SmartScreen reputation on every new release, so flagging can recur.

If a release is flagged:

1. **Submit the exact `.exe` to Microsoft** as a false positive: https://www.microsoft.com/en-us/wdsi/filesubmission. Mark it "Software developer" → "Incorrectly detected." Turnaround is usually 24-48h; once accepted, a definitions update whitelists the SHA-256 globally.
2. **Restore from quarantine and re-test.** Defender caches verdicts by hash, so a clean rebuild produces a new hash that has to be re-evaluated.
3. **Long-term**: a code-signing certificate (OV ~$200/yr, EV ~$400/yr) is the only durable fix. EV builds SmartScreen reputation immediately; OV accrues it over downloads.

Operators installing on lab machines can also add `C:\Tools\SerialHop\` (or wherever the `.exe` lives) to Defender's exclusion list as a stopgap.

## Run modes

The single binary detects how it was launched and behaves accordingly:

| Launched via               | Mode               |
| -------------------------- | ------------------ |
| SCM (after install)        | Service worker     |
| Double-click               | Control panel      |
| `--admin-action=...` (UAC) | Internal: SCM op   |
| `--foreground`             | Console developer mode (legacy behavior; JSON logs to stdout, Ctrl-C to stop) |

## REST API

The REST API is bound to `127.0.0.1` on the lab machine; it is reachable from outside **only** through the chisel reverse tunnel.

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/discover` | Run a fresh discovery and return the device list |
| `GET`  | `/devices`  | Return the cached device list |
| `POST` | `/devices/{id}/command` | Send raw bytes; optionally read a reply |

Discovered device types: `pump` (type code 10), `valve` (30), `densitometer` (70). See [`docs/superpowers/specs/2026-04-26-lab-devices-client-design.md`](docs/superpowers/specs/2026-04-26-lab-devices-client-design.md) for full request/response shapes and behavior.

## Log streaming to Loki

In service mode, the client streams every line written to `SerialHop.log` and `SerialHop_stderr.log` to the in-VPS Loki via a forward tunnel (`127.0.0.1:3100 → loki:3100`) added to the same chisel session. The on-disk rotated files remain the durable record; Loki is a queryable mirror.

- Gated on `chisel.user` being set — without auth the server's per-user allowlist won't grant the route, and the shipper is a no-op (a one-time `slog.Warn` is emitted at startup).
- In-memory ring buffer (10 000 records, drop-oldest on overflow) decouples disk writes from network. Pushes are gzipped JSON, batched up to 500 records or 2 s, with backoff retry on 5xx and drop-batch on 4xx.
- Labels: `client` (chisel user), `stream` (`stdout`/`stderr`), `service` (`serialhop`), `version`. Filter on these in Grafana.

Foreground developer mode does not ship — stdout there is a real terminal the developer is already watching. See [`docs/superpowers/specs/2026-04-28-log-streaming-design.md`](docs/superpowers/specs/2026-04-28-log-streaming-design.md) for the full design.

## Tests

```
task test
```

Tests run on macOS and Windows. The Windows-only files (service worker, real SCM client, walk panel, UAC elevation helpers) are silently skipped on non-Windows hosts; their logic is covered by tests against fakes.

## Security

Threat model and vulnerability-reporting instructions are in [`SECURITY.md`](SECURITY.md). It also addresses the "is this a RAT?" question in detail.

## License

Apache-2.0 — see [`LICENSE`](LICENSE).
