// SerialHop — Devices, Ports, Logs tabs
/* global React, Button, Help */

function Toolbar({ banner, banner_html, actions }) {
  return (
    <div className="shp-toolbar">
      <div className="shp-toolbar__banner">
        {banner_html ? <span dangerouslySetInnerHTML={{ __html: banner_html }} /> : banner}
      </div>
      <div className="shp-btn-row">{actions}</div>
    </div>
  );
}

function DevicesTab({ state, devices, lastDiscovered, openHelpId }) {
  // state: 'ok' | 'service-down' | 'never-discovered' | 'unreachable' | 'discovering'
  const isOk = state === "ok";
  const isDiscovering = state === "discovering";
  const allDisabled = state === "service-down" || state === "unreachable";

  let banner_html = "";
  if (state === "service-down") banner_html = `Service is not running. Start it from the <b>Status</b> tab.`;
  else if (state === "never-discovered") banner_html = `No devices yet. Click <b>Rediscover</b> to probe serial ports.`;
  else if (state === "unreachable") banner_html = `Can't reach the local service. It may have just started — wait a few seconds and click <b>Refresh</b>.`;
  else if (state === "discovering") banner_html = `<b>Probing serial ports…</b> closing existing connections.`;
  else banner_html = `<code>Discovered at ${lastDiscovered}</code> · <b>${devices.length}</b> devices`;

  return (
    <div>
      <Toolbar
        banner_html={banner_html}
        actions={
          <>
            <Button variant="primary" disabled={allDisabled || isDiscovering}>↻ Rediscover</Button>
            <Button variant="danger" disabled={!isOk || isDiscovering || devices.length === 0}>Disconnect all</Button>
            <Button disabled={state === "service-down" || isDiscovering}>Refresh</Button>
          </>
        }
      />

      {isOk || isDiscovering ? (
        <div className="shp-table-wrap" style={{ opacity: isDiscovering ? 0.5 : 1, transition: "opacity .2s" }}>
          <table className="shp-table">
            <thead>
              <tr>
                <th style={{ width: "30%" }}>ID</th>
                <th style={{ width: "30%" }}>Type</th>
                <th style={{ width: "30%" }}>Port</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {devices.map(d => (
                <tr key={d.id}>
                  <td><b style={{ color: "var(--text)" }}>{d.id}</b></td>
                  <td>{d.type}</td>
                  <td>{d.port}</td>
                  <td></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <DevicesEmpty state={state} />
      )}
    </div>
  );
}

function DevicesEmpty({ state }) {
  const messages = {
    "service-down": {
      icon: "○ ○ ○",
      title: "Service is not running.",
      body: "Start the SerialHop service from the Status tab. Devices will appear here once it's running and discovery has completed.",
    },
    "never-discovered": {
      icon: "—",
      title: "No devices have been discovered yet.",
      body: "Click Rediscover to probe the available serial ports. This may take several seconds; existing connections will be closed.",
    },
    "unreachable": {
      icon: "?",
      title: "Can't reach the local service.",
      body: "It may have just started. Wait a few seconds and click Refresh. If the problem persists, check the service from the Status tab.",
    },
  };
  const m = messages[state] || messages["service-down"];
  return (
    <div className="shp-empty">
      <div className="shp-empty__icon">{m.icon}</div>
      <div className="shp-empty__title">{m.title}</div>
      <div className="shp-empty__body">{m.body}</div>
    </div>
  );
}

function PortsTab({ ports, openHelpId }) {
  const matched = ports.filter(p => p.discovered).length;
  return (
    <div>
      <Toolbar
        banner_html={`<b>${ports.length}</b> ports enumerated by the OS · <b>${matched}</b> matched to a device`}
        actions={
          <>
            <Button>↻ Refresh</Button>
            <Button variant="primary">Rediscover</Button>
          </>
        }
      />
      <div className="shp-table-wrap">
        <table className="shp-table">
          <thead>
            <tr>
              <th>Name</th>
              <th>USB</th>
              <th>
                VID
                <Help id="hdr-vid" openHelpId={openHelpId} helpProps={{
                  title: "Vendor ID (VID)",
                  what: "USB vendor ID assigned by USB-IF, shown in hex.",
                  when: "Useful when matching against driver INF files or vendor docs.",
                }} />
              </th>
              <th>
                PID
                <Help id="hdr-pid" openHelpId={openHelpId} helpProps={{
                  title: "Product ID (PID)",
                  what: "Per-vendor product identifier, in hex.",
                  when: "Together with VID, uniquely identifies the device model.",
                }} />
              </th>
              <th>
                Serial №
                <Help id="hdr-sn" openHelpId={openHelpId} helpProps={{
                  title: "Serial number",
                  what: "Per-unit serial string the device reports over USB.",
                  when: "Not every device reports one — blank is normal.",
                }} />
              </th>
              <th>
                Product
                <Help id="hdr-prod" openHelpId={openHelpId} helpProps={{
                  title: "Product descriptor",
                  what: "Human-readable product string the device advertises.",
                }} />
              </th>
              <th>
                Discovered
                <Help id="hdr-disc" openHelpId={openHelpId} helpProps={{
                  title: "Discovered",
                  what: "A check means SerialHop matched a logical device on this port.",
                  when: "Blank means the OS sees the port but discovery didn't recognise anything on it.",
                }} />
              </th>
              <th>
                Device ID
                <Help id="hdr-did" openHelpId={openHelpId} helpProps={{
                  title: "Device ID",
                  what: "The logical ID the matched device is addressed by.",
                  when: "Empty when nothing was matched on this port.",
                }} />
              </th>
            </tr>
          </thead>
          <tbody>
            {ports.map(p => (
              <tr key={p.name} data-selected={p.selected}>
                <td><b style={{ color: "var(--text)" }}>{p.name}</b></td>
                <td>{p.usb ? <span className="shp-check">✓</span> : <span className="shp-dim">—</span>}</td>
                <td>{p.vid || <span className="shp-dim">—</span>}</td>
                <td>{p.pid || <span className="shp-dim">—</span>}</td>
                <td>{p.serial || <span className="shp-dim">—</span>}</td>
                <td style={{ whiteSpace: "normal", fontFamily: "'IBM Plex Sans', system-ui, sans-serif" }}>
                  {p.product || <span className="shp-dim">—</span>}
                </td>
                <td>{p.discovered ? <span className="shp-check">✓</span> : <span className="shp-dim">—</span>}</td>
                <td>{p.deviceId || <span className="shp-dim">—</span>}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function LogsTab({ stream, levelFilter, follow, search, lines, selectedIdx, openHelpId, mode = "table" }) {
  const isService = stream === "service";
  return (
    <div>
      <div className="shp-logs-controls">
        <label className="shp-row" style={{ gap: 6 }}>
          <span className="shp-muted" style={{ fontSize: 11.5, fontWeight: 500 }}>Stream</span>
          <Help id="logs-stream" openHelpId={openHelpId} helpProps={{
            title: stream === "service" ? "Service log" : stream === "stderr" ? "Stderr" : "Panel errors",
            what:
              stream === "service" ? "Structured log records the service writes to disk."
              : stream === "stderr" ? "Raw stderr output from the service — panic traces and lower-level errors."
              : "Errors the panel itself logged. Useful when the panel UI misbehaves.",
            when: stream === "service" ? "Filter by level using the Level dropdown." : "No level metadata, so the Level filter is ignored.",
          }} />
          <select className="shp-select" defaultValue={stream} style={{ width: 160 }}>
            <option value="service">Service log</option>
            <option value="stderr">Stderr</option>
            <option value="panel">Panel errors</option>
          </select>
        </label>

        <label className="shp-row" style={{ gap: 6 }}>
          <span className="shp-muted" style={{ fontSize: 11.5, fontWeight: 500 }}>Level</span>
          <select className="shp-select" defaultValue={levelFilter} disabled={!isService} style={{ width: 110 }}>
            <option value="all">all</option>
            <option value="debug">debug</option>
            <option value="info">info</option>
            <option value="warn">warn</option>
            <option value="error">error</option>
          </select>
        </label>

        <div className="shp-toggle" data-on={follow}>
          <span className="shp-toggle__sw"></span>
          <span>Follow</span>
        </div>

        <input className="shp-input shp-input--mono" placeholder="Search…" defaultValue={search}
          style={{ width: 220 }} />

        <span className="shp-gap" />

        <Button variant="ghost">Open logs folder ↗</Button>
      </div>

      {isService && mode === "table" ? (
        <div className="shp-table-wrap" style={{ maxHeight: 420, overflow: "auto" }}>
          <table className="shp-table shp-logs-table">
            <thead>
              <tr>
                <th className="col-time">Time</th>
                <th className="col-level">Level</th>
                <th>Message</th>
              </tr>
            </thead>
            <tbody>
              {lines.map((l, i) => (
                <React.Fragment key={i}>
                  <tr data-selected={i === selectedIdx}>
                    <td className="col-time">{l.time}</td>
                    <td><span className="shp-level-pill" data-level={l.level}>{l.level}</span></td>
                    <td style={{ whiteSpace: "pre" }}>
                      {highlightSearch(l.msg, search)}
                    </td>
                  </tr>
                  {i === selectedIdx && l.fields && (
                    <tr className="shp-logs-detail">
                      <td colSpan={3}>
                        <dl className="shp-kv">
                          {Object.entries(l.fields).map(([k, v]) => (
                            <React.Fragment key={k}>
                              <dt>{k}</dt><dd>{v}</dd>
                            </React.Fragment>
                          ))}
                        </dl>
                      </td>
                    </tr>
                  )}
                </React.Fragment>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <pre className="shp-mono-view">
          {lines.map((l, i) => (
            <span key={i}>
              <span className="ln-time">{l.time} </span>
              <span className={l.kind ? `ln-${l.kind}` : ""}>{l.text}</span>
              {"\n"}
            </span>
          ))}
        </pre>
      )}
    </div>
  );
}

function highlightSearch(text, q) {
  if (!q) return text;
  const i = text.toLowerCase().indexOf(q.toLowerCase());
  if (i === -1) return text;
  return (
    <>
      {text.slice(0, i)}
      <mark style={{ background: "var(--warning-soft)", color: "var(--text)", padding: "0 2px", borderRadius: 2 }}>
        {text.slice(i, i + q.length)}
      </mark>
      {text.slice(i + q.length)}
    </>
  );
}

Object.assign(window, { DevicesTab, PortsTab, LogsTab });
