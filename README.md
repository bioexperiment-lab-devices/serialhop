# lab_devices_client

Single-binary Go application that exposes serial-port lab devices to a remote HTTP client through a chisel reverse tunnel.

## Build

Default target is Windows / amd64:
```
task build
```

Override target via env variables:
```
task build GOOS=linux GOARCH=arm64
```

Output: `dist/lab_devices_client[.exe]`.

## First run

The binary expects a `lab_devices_client_config.yaml` next to itself. On first run the binary writes a scaffold and exits:

```
> lab_devices_client.exe
Config file created at C:\path\to\lab_devices_client_config.yaml. Please review and edit it, then run again.
```

Edit the file (set `chisel.remote_port` to a unique port for this machine) and run again.

## REST API

The REST API is bound to `127.0.0.1` locally; it is reachable from outside the lab machine **only** through the chisel reverse tunnel.

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/discover` | Run a fresh discovery and return the device list |
| `GET`  | `/devices`  | Return the cached device list |
| `POST` | `/devices/{id}/command` | Send raw bytes; optionally read a reply |

See `docs/superpowers/specs/2026-04-26-lab-devices-client-design.md` for full request/response shapes and behavior.

## Tests

```
task test
```
