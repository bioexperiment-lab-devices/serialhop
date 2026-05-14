// SerialHop — canvas composition
/* global React, ReactDOM, DesignCanvas, DCSection, DCArtboard,
   PanelWindow, StatusTab, ConfigTab, DevicesTab, PortsTab, LogsTab,
   Modal, Button, TweaksPanel, useTweaks, TweakSection, TweakRadio, TweakColor */

const VERSION = "1.2.1";
const W = 1080, H = 680;

// ---------- Sample data ----------
const SAMPLE_DEVICES = [
  { id: "balance_1",    type: "balance",   port: "COM3"  },
  { id: "incubator_1",  type: "incubator", port: "COM4"  },
  { id: "pump_1",       type: "pump",      port: "COM5"  },
  { id: "pump_2",       type: "pump",      port: "COM7"  },
  { id: "stirplate_1",  type: "stirplate", port: "COM8"  },
  { id: "thermo_1",     type: "thermometer", port: "COM11" },
];

const SAMPLE_PORTS = [
  { name: "COM1",  usb: false, vid: "",     pid: "",     serial: "",          product: "",                              discovered: false, deviceId: "" },
  { name: "COM3",  usb: true,  vid: "0x0403", pid: "0x6001", serial: "AB0KLM3R", product: "FT232R USB UART",            discovered: true,  deviceId: "balance_1", selected: true },
  { name: "COM4",  usb: true,  vid: "0x10C4", pid: "0xEA60", serial: "0001-0003", product: "CP2102 USB to UART Bridge", discovered: true,  deviceId: "incubator_1" },
  { name: "COM5",  usb: true,  vid: "0x2341", pid: "0x0043", serial: "5573731", product: "Arduino Uno",                 discovered: true,  deviceId: "pump_1" },
  { name: "COM6",  usb: true,  vid: "0x1A86", pid: "0x7523", serial: "",        product: "CH340 Serial",                discovered: false, deviceId: "" },
  { name: "COM7",  usb: true,  vid: "0x2341", pid: "0x0043", serial: "8821049", product: "Arduino Uno",                 discovered: true,  deviceId: "pump_2" },
  { name: "COM8",  usb: true,  vid: "0x16C0", pid: "0x0483", serial: "T_4521A", product: "Teensy 3.2",                  discovered: true,  deviceId: "stirplate_1" },
  { name: "COM11", usb: true,  vid: "0x0403", pid: "0x6015", serial: "DK0RFA2P", product: "FT231X USB UART",            discovered: true,  deviceId: "thermo_1" },
  { name: "COM12", usb: false, vid: "",     pid: "",     serial: "",          product: "",                              discovered: false, deviceId: "" },
];

const SERVICE_LOGS = [
  { time: "15:04:07.012", level: "info",  msg: "service starting (v1.2.1, pid=18472)",
    fields: { component: "main", version: "1.2.1", pid: 18472, datadir: "C:/ProgramData/SerialHop" } },
  { time: "15:04:07.084", level: "info",  msg: "REST listening on 127.0.0.1:53117",
    fields: { component: "rest", addr: "127.0.0.1:53117", picked_port: "true" } },
  { time: "15:04:07.151", level: "info",  msg: "tunnel: connecting to 111.88.145.138:443",
    fields: { component: "tunnel", host: "111.88.145.138", port: 443 } },
  { time: "15:04:07.612", level: "info",  msg: "tunnel: connected (session=7c1f…)",
    fields: { component: "tunnel", session_id: "7c1fA3", remote_port: 29017 } },
  { time: "15:04:08.001", level: "info",  msg: "discovery: probing 9 ports",
    fields: { component: "discovery", ports: 9, include: "[]", exclude: "[]" } },
  { time: "15:04:08.412", level: "debug", msg: "probe COM3 → balance v2.1 (0.40s)",
    fields: { component: "discovery", port: "COM3", probe: "balance.v2", elapsed_ms: 401 } },
  { time: "15:04:09.211", level: "debug", msg: "probe COM5 → pump v1.4 (0.80s)",
    fields: { component: "discovery", port: "COM5", probe: "pump.v1", elapsed_ms: 799 } },
  { time: "15:04:10.052", level: "warn",  msg: "probe COM6: no response after 3 tries — skipping",
    fields: { component: "discovery", port: "COM6", attempts: 3, last_error: "timeout" } },
  { time: "15:04:11.119", level: "info",  msg: "discovery: 6 of 9 ports matched (3.12s)",
    fields: { component: "discovery", matched: 6, total: 9, elapsed_ms: 3118 } },
  { time: "15:04:14.882", level: "error", msg: "pump_2: write failed: ERR_TIMEOUT (retry 1/3)",
    fields: { component: "device", device: "pump_2", port: "COM7", err: "ERR_TIMEOUT", retry: 1 } },
  { time: "15:04:15.901", level: "info",  msg: "pump_2: recovered after 2 retries",
    fields: { component: "device", device: "pump_2", retries: 2, total_ms: 1019 } },
  { time: "15:07:11.044", level: "info",  msg: "service restart requested by panel",
    fields: { component: "main", initiator: "panel", uid: "operator" } },
];

const STDERR_LINES = [
  { time: "15:01:18", text: "Sentry initialized: project=serialhop env=prod" },
  { time: "15:01:18", text: "loaded config from C:/ProgramData/SerialHop/config.toml" },
  { time: "15:01:19", text: "panic recovered in tunnel.keepalive — restarting goroutine", kind: "warn" },
  { time: "15:01:19", text: "goroutine 41 [running]:" },
  { time: "15:01:19", text: "  serialhop/tunnel.(*Session).heartbeat(0xc0001a8000)" },
  { time: "15:01:19", text: "        /build/tunnel/session.go:184 +0x12c" },
  { time: "15:01:19", text: "  created by serialhop/tunnel.(*Session).run" },
  { time: "15:01:19", text: "        /build/tunnel/session.go:97 +0x84" },
  { time: "15:01:20", text: "tunnel.keepalive: restarted after panic (count=1/3)" },
  { time: "15:01:22", text: "rest: bound to 127.0.0.1:53117 (auto-picked)" },
  { time: "15:01:22", text: "discovery: starting initial probe (9 ports)" },
  { time: "15:01:25", text: "discovery: COM6 probe timed out after 3 attempts", kind: "warn" },
  { time: "15:01:25", text: "device pump_2: ERR_TIMEOUT on first write — see service log", kind: "err" },
  { time: "15:01:26", text: "device pump_2: recovered" },
];

// ---------- Footer presets ----------
const FOOTERS = {
  saved:        { kind: "ok",   text: "<b>Saved.</b> Restart the service to apply.", time: "15:02:18" },
  service_ok:   { kind: "ok",   text: "Service restarted",                            time: "15:07:11" },
  installed:    { kind: "ok",   text: "Service installed",                            time: "15:04:23" },
  working:      { kind: "work", text: "Working…",                                     time: "15:07:09" },
  downloading:  { kind: "work", text: "Downloading <b>42%</b> (3.1 / 7.4 MB)",        time: "15:09:21", progress: 42 },
  cancelled:    { kind: "info", text: "Cancelled.",                                   time: "15:09:30" },
  failed_save:  { kind: "err",  text: "Failed: <b>Server rejected these credentials.</b>", time: "15:02:44" },
  idle:         { kind: "info", text: "Ready.",                                       time: "15:00:01" },
  note_unsaved: { kind: "info", text: "Note: unsaved config changes were not applied.", time: "15:07:11" },
  first_run:    { kind: "info", text: "Welcome. Fill in lab-bridge credentials to begin.", time: "15:00:01" },
};

// ---------- Config defaults ----------
const SAVED_CFG = {
  host: "111.88.145.138",
  username: "bench-04",
  password: "h3xagon-tide-bowline",
  rest_port: 0,
  include: ["COM3", "COM4", "COM5", "COM7", "COM8", "COM11"],
  exclude: [],
  settle_ms: 2000,
  log_level: "info",
  raw_enabled: false,
  autoupdate: true,
  flash_enabled: false,
  flash_dir: "",
  flash_keep: 10,
};

const FRESH_CFG = {
  host: "111.88.145.138",
  username: "",
  password: "",
  rest_port: 0,
  include: [],
  exclude: [],
  settle_ms: 2000,
  log_level: "info",
  raw_enabled: false,
  autoupdate: true,
  flash_enabled: false,
  flash_dir: "",
  flash_keep: 10,
};

// ---------- Lamp presets ----------
const LAMP_PRESETS = {
  running: {
    service: { tone: "green", label: "Running",   sub: "pid 18472 · 00:24:51" },
    server:  { tone: "green", label: "Up",        sub: "111.88.145.138 · 142ms" },
    tunnel:  { tone: "green", label: "Connected", sub: "remote port 29017" },
  },
  not_installed: {
    service: { tone: "red",  label: "Not installed", sub: "config invalid" },
    server:  { tone: "grey", label: "—",             sub: "service down" },
    tunnel:  { tone: "grey", label: "Not configured", sub: "—" },
  },
  transitioning: {
    service: { tone: "grey", label: "Starting…",  sub: "elevation prompt", pulse: true },
    server:  { tone: "grey", label: "Checking…",  sub: "—",                pulse: true },
    tunnel:  { tone: "grey", label: "Checking…",  sub: "—",                pulse: true },
  },
  tunnel_down: {
    service: { tone: "green", label: "Running",   sub: "pid 18472 · 02:14:09" },
    server:  { tone: "green", label: "Up",        sub: "111.88.145.138 · 138ms" },
    tunnel:  { tone: "red",   label: "Auth failed", sub: "401 from chisel" },
  },
};

// ============================================================
// Artboard renderers
// ============================================================

function ArtStatus_Running() {
  return (
    <PanelWindow version={VERSION} activeTab="status" footer={FOOTERS.service_ok}>
      <StatusTab
        lamps={LAMP_PRESETS.running}
        serviceState="installed"
        update={{ stage: "available", version: "1.3.0" }}
      />
    </PanelWindow>
  );
}

function ArtStatus_Downloading() {
  return (
    <PanelWindow version={VERSION} activeTab="status" footer={FOOTERS.downloading}>
      <StatusTab
        lamps={LAMP_PRESETS.running}
        serviceState="installed"
        update={{ stage: "downloading", version: "1.3.0", downloaded: "3.1 MB", total: "7.4 MB", percent: 42 }}
      />
    </PanelWindow>
  );
}

function ArtStatus_NotInstalled() {
  return (
    <PanelWindow version={VERSION} activeTab="status"
      warning="Configuration is missing the lab-bridge password. The service can't start until both Username and Password are saved on the Config tab."
      footer={FOOTERS.first_run}>
      <StatusTab
        lamps={LAMP_PRESETS.not_installed}
        serviceState="not-installed"
      />
    </PanelWindow>
  );
}

function ArtStatus_TunnelDown() {
  return (
    <PanelWindow version={VERSION} activeTab="status" footer={FOOTERS.idle}>
      <StatusTab
        lamps={LAMP_PRESETS.tunnel_down}
        serviceState="installed"
        update={{ stage: "ready", version: "1.3.0" }}
      />
    </PanelWindow>
  );
}

function ArtStatus_HelpOpen() {
  return (
    <PanelWindow version={VERSION} activeTab="status" footer={FOOTERS.installed}>
      <StatusTab
        lamps={LAMP_PRESETS.running}
        serviceState="installed"
        update={{ stage: "installed", version: "1.3.0" }}
        openHelpId="lamp-tunnel"
      />
    </PanelWindow>
  );
}

function ArtConfig_FirstLaunch() {
  return (
    <PanelWindow version={VERSION} activeTab="config" dirty footer={FOOTERS.first_run}>
      <ConfigTab values={FRESH_CFG} dirty showFirstLaunch />
    </PanelWindow>
  );
}

function ArtConfig_Saved() {
  return (
    <PanelWindow version={VERSION} activeTab="config" footer={FOOTERS.saved}>
      <ConfigTab values={SAVED_CFG} dirty={false} openHelpId="cfg-host" />
    </PanelWindow>
  );
}

function ArtConfig_Errors() {
  const errs = {
    host: "Must be a hostname or IP.",
    flash_dir: "Required when flashing is enabled — must be an absolute path.",
  };
  const v = {
    ...SAVED_CFG,
    host: "111.88..145.138",
    flash_enabled: true,
    flash_dir: "",
  };
  return (
    <PanelWindow version={VERSION} activeTab="config" dirty footer={FOOTERS.failed_save}>
      <ConfigTab values={v} errors={errs} dirty credentialError />
    </PanelWindow>
  );
}

function ArtConfig_VerifyModal() {
  return (
    <PanelWindow version={VERSION} activeTab="config" dirty footer={FOOTERS.working}
      modal={
        <Modal
          title="Couldn't reach the lab-bridge"
          sub="Save credentials without verifying?"
          actions={
            <>
              <Button>Cancel</Button>
              <Button variant="primary">Save anyway</Button>
            </>
          }
        >
          The panel couldn't reach <code>111.88.145.138</code> to verify the new credentials
          (<code>dial tcp: i/o timeout after 8s</code>).
          <br /><br />
          You can save them anyway and the service will retry on its next start. If the credentials
          are wrong, the Tunnel lamp will turn red once the server comes back.
        </Modal>
      }>
      <ConfigTab values={{ ...SAVED_CFG, username: "bench-04b", password: "new-passphrase-here" }} dirty />
    </PanelWindow>
  );
}

function ArtConfig_UnsavedGuard() {
  return (
    <PanelWindow version={VERSION} activeTab="config" dirty footer={FOOTERS.idle}
      modal={
        <Modal
          title="Discard unsaved configuration changes?"
          sub="You've edited 3 fields since the last save."
          actions={
            <>
              <Button>Cancel</Button>
              <Button variant="danger">Discard</Button>
              <Button variant="primary">Save</Button>
            </>
          }
        >
          You're about to switch to the <b>Status</b> tab. Your pending edits to <b>Username</b>,
          <b> Password</b>, and <b>Post-open settle</b> haven't been written yet — choose what to do
          with them before continuing.
        </Modal>
      }>
      <ConfigTab values={{ ...SAVED_CFG, settle_ms: 3500 }} dirty />
    </PanelWindow>
  );
}

function ArtDevices_Populated() {
  return (
    <PanelWindow version={VERSION} activeTab="devices" footer={FOOTERS.service_ok}>
      <DevicesTab state="ok" devices={SAMPLE_DEVICES} lastDiscovered="15:04:11" />
    </PanelWindow>
  );
}

function ArtDevices_ServiceDown() {
  return (
    <PanelWindow version={VERSION} activeTab="devices"
      warning="The local SerialHop service stopped responding. Restart it from the Status tab."
      footer={FOOTERS.idle}>
      <DevicesTab state="service-down" devices={[]} />
    </PanelWindow>
  );
}

function ArtDevices_Never() {
  return (
    <PanelWindow version={VERSION} activeTab="devices" footer={FOOTERS.installed}>
      <DevicesTab state="never-discovered" devices={[]} />
    </PanelWindow>
  );
}

function ArtDevices_Discovering() {
  return (
    <PanelWindow version={VERSION} activeTab="devices" footer={{
      kind: "work", text: "Probing serial ports — closing existing connections", time: "15:04:09",
    }}>
      <DevicesTab state="discovering" devices={SAMPLE_DEVICES.slice(0, 3)} lastDiscovered="15:04:11" />
    </PanelWindow>
  );
}

function ArtPorts_Populated() {
  return (
    <PanelWindow version={VERSION} activeTab="ports" footer={FOOTERS.service_ok}>
      <PortsTab ports={SAMPLE_PORTS} openHelpId="hdr-disc" />
    </PanelWindow>
  );
}

function ArtLogs_Service() {
  return (
    <PanelWindow version={VERSION} activeTab="logs" footer={FOOTERS.idle}>
      <LogsTab
        stream="service"
        levelFilter="all"
        follow={true}
        search="discovery"
        lines={SERVICE_LOGS}
        selectedIdx={8}
        mode="table"
      />
    </PanelWindow>
  );
}

function ArtLogs_Stderr() {
  return (
    <PanelWindow version={VERSION} activeTab="logs" footer={FOOTERS.idle}>
      <LogsTab
        stream="stderr"
        levelFilter="all"
        follow={false}
        search=""
        lines={STDERR_LINES}
        mode="mono"
      />
    </PanelWindow>
  );
}

// ============================================================
// Canvas
// ============================================================

const ARTBOARDS = [
  // Status tab
  { sec: "Status tab", id: "status-running",        label: "01 · Running, update available",      C: ArtStatus_Running },
  { sec: "Status tab", id: "status-downloading",    label: "02 · Update downloading",             C: ArtStatus_Downloading },
  { sec: "Status tab", id: "status-help-popover",   label: "03 · Update installed + help popover",C: ArtStatus_HelpOpen },
  { sec: "Status tab", id: "status-tunnel-down",    label: "04 · Tunnel auth failed",             C: ArtStatus_TunnelDown },
  { sec: "Status tab", id: "status-not-installed",  label: "05 · Service not installed",          C: ArtStatus_NotInstalled },

  // Config tab
  { sec: "Config tab", id: "config-first",          label: "06 · First launch (empty creds)",     C: ArtConfig_FirstLaunch },
  { sec: "Config tab", id: "config-saved",          label: "07 · Saved config + help open",       C: ArtConfig_Saved },
  { sec: "Config tab", id: "config-errors",         label: "08 · Validation + creds rejected",    C: ArtConfig_Errors },
  { sec: "Config tab", id: "config-verify",         label: "09 · Verify-then-save modal",         C: ArtConfig_VerifyModal },
  { sec: "Config tab", id: "config-unsaved",        label: "10 · Unsaved-changes guard",          C: ArtConfig_UnsavedGuard },

  // Devices + Ports
  { sec: "Devices & Ports", id: "devices-ok",       label: "11 · Devices — populated",            C: ArtDevices_Populated },
  { sec: "Devices & Ports", id: "devices-never",    label: "12 · Devices — never discovered",     C: ArtDevices_Never },
  { sec: "Devices & Ports", id: "devices-discovering", label: "13 · Devices — discovering",       C: ArtDevices_Discovering },
  { sec: "Devices & Ports", id: "devices-down",     label: "14 · Devices — service down",         C: ArtDevices_ServiceDown },
  { sec: "Devices & Ports", id: "ports-ok",         label: "15 · Ports — full table",             C: ArtPorts_Populated },

  // Logs
  { sec: "Logs", id: "logs-service", label: "16 · Service log — table view, row expanded", C: ArtLogs_Service },
  { sec: "Logs", id: "logs-stderr",  label: "17 · Stderr — raw text view",                 C: ArtLogs_Stderr },
];

const SECTIONS = [
  { id: "status",   title: "Status tab"        },
  { id: "config",   title: "Config tab"        },
  { id: "devices",  title: "Devices & Ports"   },
  { id: "logs",     title: "Logs"              },
];

const DEFAULT_TWEAKS = /*EDITMODE-BEGIN*/{
  "theme": "light",
  "accent": "#1F3A8A"
}/*EDITMODE-END*/;

function App() {
  const [t, setTweak] = useTweaks(DEFAULT_TWEAKS);

  React.useEffect(() => {
    document.documentElement.dataset.theme = t.theme;
    document.documentElement.style.setProperty("--accent", t.accent);
    document.documentElement.style.setProperty("--accent-hover", shade(t.accent, -14));
  }, [t.theme, t.accent]);

  return (
    <>
      <DesignCanvas>
        {SECTIONS.map(s => (
          <DCSection id={s.id} title={s.title} key={s.id}>
            {ARTBOARDS.filter(a => sectionOf(a) === s.id).map(a => (
              <DCArtboard id={a.id} key={a.id} label={a.label} width={W} height={H}>
                <a.C />
              </DCArtboard>
            ))}
          </DCSection>
        ))}
      </DesignCanvas>

      <TweaksPanel title="Tweaks">
        <TweakSection label="Theme">
          <TweakRadio label="Mode" value={t.theme} onChange={v => setTweak("theme", v)}
            options={[{ value: "light", label: "Light" }, { value: "dark", label: "Dark" }]} />
        </TweakSection>
        <TweakSection label="Accent">
          <TweakColor label="Color" value={t.accent} onChange={v => setTweak("accent", v)}
            options={["#1F3A8A", "#3B5BA5", "#1F6E62", "#7A3F00", "#5B1F66"]} />
        </TweakSection>
      </TweaksPanel>
    </>
  );
}

function sectionOf(a) {
  if (a.sec === "Status tab") return "status";
  if (a.sec === "Config tab") return "config";
  if (a.sec === "Devices & Ports") return "devices";
  return "logs";
}

function shade(hex, pct) {
  // simple % lightness shift on hex
  const num = parseInt(hex.slice(1), 16);
  let r = (num >> 16) & 0xff, g = (num >> 8) & 0xff, b = num & 0xff;
  const adj = (c) => Math.max(0, Math.min(255, Math.round(c + (pct/100) * 255)));
  r = adj(r); g = adj(g); b = adj(b);
  return "#" + [r,g,b].map(x => x.toString(16).padStart(2, "0")).join("");
}

ReactDOM.createRoot(document.getElementById("root")).render(<App />);
