# lab_devices_client

Single-binary Go application that exposes serial-port lab devices to a remote HTTP client through a chisel reverse tunnel. Runs as a Windows service; managed through a small native control-panel window.

## Build

Default target is Windows / amd64:
```
task build
```

Override target via env variables:
```
task build GOOS=linux GOARCH=arm64
```

Output: `dist/lab_devices_client.exe`.

The build embeds an icon, a UAC manifest (`asInvoker`), and version metadata via `goversioninfo`. The first build downloads `goversioninfo` automatically.

## Install on a Windows lab machine

1. Copy `lab_devices_client.exe` to an install location (e.g., `C:\Tools\LabDevicesClient\`).
2. Double-click the .exe. The control panel opens. On first launch it writes `lab_devices_client_config.yaml` next to the .exe and shows a validation warning if anything's wrong.
3. Click **Open config file**, set `chisel.remote_port` (and any other site-specific values), save.
4. Click **Install**. UAC prompts; approve. The service is registered as `LabDevicesClient` (auto-start at boot, runs as LocalSystem) and started immediately.

After install:

- The service runs across reboots without the panel being open.
- To apply config changes: edit the YAML file, then click **Restart** in the panel.
- To remove: click **Uninstall** in the panel.
- Logs go to `lab_devices_client.log` next to the .exe (rotated at 10 MB, 3 backups). Click **Open log file** to view.

## Run modes

The single binary detects how it was launched and behaves accordingly:

| Launched via               | Mode               |
| -------------------------- | ------------------ |
| SCM (after install)        | Service worker     |
| Double-click               | Control panel      |
| `--admin-action=...` (UAC) | Internal: SCM op   |
| `--foreground`             | Console developer mode (legacy behavior; JSON logs to stdout, Ctrl-C to stop) |

## REST API

(Unchanged from the prior design.) The REST API is bound to `127.0.0.1` on the lab machine; it is reachable from outside **only** through the chisel reverse tunnel.

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/discover` | Run a fresh discovery and return the device list |
| `GET`  | `/devices`  | Return the cached device list |
| `POST` | `/devices/{id}/command` | Send raw bytes; optionally read a reply |

See [`docs/superpowers/specs/2026-04-26-lab-devices-client-design.md`](docs/superpowers/specs/2026-04-26-lab-devices-client-design.md) for full request/response shapes and behavior.

## Tests

```
task test
```

Tests run on macOS and Windows. The Windows-only files (service worker, real SCM client, walk panel) are silently skipped on non-Windows hosts; their logic is covered by tests against fakes.
