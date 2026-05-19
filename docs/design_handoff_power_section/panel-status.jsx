// SerialHop — Status tab content
/* global React, Lamp, Button, Help */

function PowerRow({ keepAwake, openHelpId }) {
  // keepAwake: { state: 'on' | 'off' | 'unreachable', inFlight?: bool }
  const { state = "off", inFlight = false } = keepAwake || {};

  const presets = {
    on:          { tone: "green", label: "On",  sub: "System will not sleep or auto-shutdown.", action: "Click to disable", disabled: inFlight },
    off:         { tone: "grey",  label: "Off", sub: "Click to keep the system awake.",         action: "Click to enable",  disabled: inFlight },
    unreachable: { tone: "grey",  label: "—",   sub: "Service unreachable",                     action: null,                disabled: true },
  };
  const cfg = presets[state];
  const actionLabel = inFlight
    ? (state === "on" ? "Disabling…" : "Enabling…")
    : cfg.action;

  return (
    <div className="shp-power-row">
      <button
        type="button"
        className="shp-lamp shp-lamp--power shp-lamp--clickable"
        data-disabled={cfg.disabled}
        disabled={cfg.disabled}
      >
        <div className="shp-lamp__row">
          <span className="shp-lamp__name">Keep system awake</span>
          <span onClick={(e) => e.stopPropagation()} style={{ display: "inline-flex" }}>
            <Help
              id="lamp-keepawake"
              openHelpId={openHelpId}
              title="Keep system awake"
              what="Prevents Windows from idling into sleep, hibernate, or scheduled automatic shutdown while the SerialHop service is running."
              when="Has no effect on user-initiated shutdown, restart, or sign-out. Cleared if the service stops, crashes, or is updated."
            />
          </span>
        </div>
        <div className="shp-lamp__state">
          <span className="shp-lamp__dot" data-tone={cfg.tone}></span>
          <div style={{ display: "flex", flexDirection: "column", minWidth: 0 }}>
            <span className="shp-lamp__label">{cfg.label}</span>
            {cfg.sub && <span className="shp-lamp__sub">{cfg.sub}</span>}
          </div>
          {actionLabel && (
            <span className="shp-lamp__action">{actionLabel}</span>
          )}
        </div>
      </button>
    </div>
  );
}

function StatusTab({ lamps, serviceState, update, openHelpId, footerNote, keepAwake }) {
  // serviceState: 'installed' | 'not-installed' | 'transitioning'
  const installed = serviceState === "installed";
  const notInstalled = serviceState === "not-installed";
  const transitioning = serviceState === "transitioning";

  return (
    <div>
      <div className="shp-h">Service health</div>
      <div className="shp-lamps">
        <Lamp
          name="Local service"
          tone={lamps.service.tone}
          label={lamps.service.label}
          sub={lamps.service.sub}
          pulse={lamps.service.pulse}
          helpId="lamp-service"
          openHelpId={openHelpId}
          helpProps={{
            title: "Local service",
            what: "Is the SerialHop background service installed and running on this machine.",
            deflt: "Green = running",
            when:
              "Red means not installed and the configuration is invalid, so the service can't start. Grey means installed but stopped, or in the middle of starting or stopping.",
          }}
        />
        <Lamp
          name="Lab-bridge server"
          tone={lamps.server.tone}
          label={lamps.server.label}
          sub={lamps.server.sub}
          pulse={lamps.server.pulse}
          helpId="lamp-server"
          openHelpId={openHelpId}
          helpProps={{
            title: "Lab-bridge server",
            what: "Whether the remote lab-bridge server is reachable and its tunnel daemon (chisel) is healthy.",
            when: "Red means the server is reachable but its tunnel daemon isn't responding. Grey means the server can't be reached at all.",
          }}
        />
        <Lamp
          name="Reverse tunnel"
          tone={lamps.tunnel.tone}
          label={lamps.tunnel.label}
          sub={lamps.tunnel.sub}
          pulse={lamps.tunnel.pulse}
          helpId="lamp-tunnel"
          openHelpId={openHelpId}
          helpProps={{
            title: "Reverse tunnel",
            what: "This machine's reverse tunnel into the lab-bridge.",
            when: "Red = disconnect or auth failure. Yellow = transient server-side error. Grey = checking, not configured, or server unreachable.",
          }}
        />
      </div>

      <div className="shp-h">Power</div>
      <PowerRow keepAwake={keepAwake} openHelpId={openHelpId} />

      <div className="shp-h">Service control</div>
      <div className="shp-service-actions">
        <Button variant="primary" elevated disabled={installed || transitioning}>Install</Button>
        <Button variant="danger" elevated disabled={!installed || transitioning}>Uninstall</Button>
        <Button elevated disabled={!installed || transitioning}>Restart</Button>
        <span className="shp-service-actions__hint">
          {notInstalled  && "↑ Install requires admin privileges"}
          {installed     && "↑ All service actions require admin privileges"}
          {transitioning && "Working — re-checking lamps…"}
        </span>
      </div>

      {footerNote && (
        <div style={{ marginTop: 14, fontSize: 11.5, color: "var(--text-muted)", fontFamily: "'IBM Plex Mono', monospace" }}>
          {footerNote}
        </div>
      )}

      {update && <UpdateRow {...update} />}
    </div>
  );
}

function UpdateRow({ stage, version, downloaded, total, percent }) {
  if (stage === "available") {
    return (
      <div className="shp-update" data-tone="blue">
        <span className="shp-update__tag">Update</span>
        <span className="shp-update__msg">
          Version <b>v{version}</b> is available.
        </span>
        <div className="shp-update__actions">
          <Button>Release notes</Button>
          <Button variant="primary">Download</Button>
        </div>
      </div>
    );
  }
  if (stage === "downloading") {
    return (
      <div className="shp-update" data-tone="blue">
        <span className="shp-update__tag">Update</span>
        <span className="shp-update__msg" style={{ flex: "0 0 auto" }}>
          <b>v{version}</b> downloading…
        </span>
        <div className="shp-update__progressbar"><i style={{ width: `${percent}%` }} /></div>
        <span className="shp-update__stats">{downloaded} / {total} · {percent}%</span>
        <div className="shp-update__actions">
          <Button variant="danger">Cancel</Button>
        </div>
      </div>
    );
  }
  if (stage === "download-failed") {
    return (
      <div className="shp-update" data-tone="red">
        <span className="shp-update__tag">Update</span>
        <span className="shp-update__msg">
          <b>v{version}</b> — download failed.
        </span>
        <div className="shp-update__actions">
          <Button>Retry</Button>
        </div>
      </div>
    );
  }
  if (stage === "ready") {
    return (
      <div className="shp-update" data-tone="blue">
        <span className="shp-update__tag">Update</span>
        <span className="shp-update__msg">
          <b>v{version}</b> ready to install.
        </span>
        <div className="shp-update__actions">
          <Button>Release notes</Button>
          <Button variant="primary" elevated>Install update</Button>
        </div>
      </div>
    );
  }
  if (stage === "installing") {
    return (
      <div className="shp-update" data-tone="blue">
        <span className="shp-update__tag">Update</span>
        <span className="shp-update__msg">Installing… service will restart automatically.</span>
        <div className="shp-update__progressbar"><i style={{ width: "62%" }} /></div>
      </div>
    );
  }
  if (stage === "installed") {
    return (
      <div className="shp-update" data-tone="green">
        <span className="shp-update__tag">Update</span>
        <span className="shp-update__msg">
          Updated to <b>v{version}</b>. Close and reopen this window to load the new panel.
        </span>
      </div>
    );
  }
  if (stage === "install-failed") {
    return (
      <div className="shp-update" data-tone="red">
        <span className="shp-update__tag">Update</span>
        <span className="shp-update__msg">
          Update failed — service restored to previous version.
        </span>
        <div className="shp-update__actions">
          <Button>Retry</Button>
        </div>
      </div>
    );
  }
  return null;
}

Object.assign(window, { StatusTab, UpdateRow, PowerRow });
