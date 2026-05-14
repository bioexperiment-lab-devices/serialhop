// SerialHop — Config tab content
/* global React, Section, Field, Button, Checkbox, Help */

function ConfigTab({
  values,
  errors = {},
  dirty,
  showFirstLaunch,
  credentialError,
  openHelpId,
}) {
  const v = values;
  const showCredErr = !!credentialError;
  const flashDisabled = !v.flash_enabled;

  return (
    <div>
      {showFirstLaunch && (
        <div className="shp-banner">
          <span className="shp-banner__chip">First launch</span>
          <span>Enter your lab-bridge credentials to enable the service.</span>
        </div>
      )}

      <Section title="Lab-bridge"
        helpId="sec-bridge" openHelpId={openHelpId}
        helpProps={{
          title: "Lab-bridge",
          what: "The remote server this machine tunnels into so the rest of the lab network can reach the devices on this host.",
        }}
      >
        <Field label="Host"
          helpId="cfg-host" openHelpId={openHelpId}
          helpProps={{
            title: "Lab-bridge host",
            what: "The address of the lab-bridge server.",
            deflt: "111.88.145.138",
            when: "Change only if your lab uses a different bridge installation.",
          }}>
          <input className="shp-input shp-input--mono" defaultValue={v.host} data-error={!!errors.host} />
          {errors.host && <div className="shp-error">{errors.host}</div>}
        </Field>

        <Field label="Username"
          helpId="cfg-user" openHelpId={openHelpId}
          helpProps={{
            title: "Lab-bridge username",
            what: "The credential issued to this machine for connecting to the lab-bridge.",
            when: "Verified against the server when saved. Required.",
          }}>
          <input className="shp-input" defaultValue={v.username}
            data-error={!!errors.username || showCredErr}
            placeholder={!v.username ? "e.g. bench-04" : undefined} />
          {errors.username && <div className="shp-error">{errors.username}</div>}
        </Field>

        <Field label="Password"
          helpId="cfg-pw" openHelpId={openHelpId}
          helpProps={{
            title: "Lab-bridge password",
            what: "Shown in plain text by design — operators sometimes need to read it back over the phone to the lab admin.",
            when: "Verified against the server when saved.",
          }}>
          <input className="shp-input shp-input--mono" defaultValue={v.password}
            data-error={!!errors.password || showCredErr}
            placeholder={!v.password ? "(plain text — not hidden)" : undefined} />
          {errors.password && <div className="shp-error">{errors.password}</div>}
          {showCredErr && (
            <div className="shp-error">Server rejected these credentials. Check the username and password.</div>
          )}
        </Field>
      </Section>

      <Section title="REST"
        helpProps={{ title: "REST", what: "The local HTTP server other lab tools on this machine talk to." }}
        helpId="sec-rest" openHelpId={openHelpId}>
        <Field label="Port"
          helpId="cfg-port" openHelpId={openHelpId}
          helpProps={{
            title: "REST port",
            what: "Local port for the HTTP API.",
            deflt: "0",
            when: "Use 0 to let the OS pick a free port. Set a specific port only if another tool needs to find SerialHop at a known address.",
          }}>
          <input className="shp-input shp-input--mono" style={{ width: 120 }}
            defaultValue={v.rest_port} />
          <div className="shp-field__hint">0 = OS picks a free port</div>
        </Field>
      </Section>

      <Section title="Discovery"
        helpProps={{ title: "Discovery", what: "How the service searches the local serial ports for known device types." }}
        helpId="sec-disc" openHelpId={openHelpId}>
        <Field label="Include list"
          helpId="cfg-incl" openHelpId={openHelpId}
          helpProps={{
            title: "Include list",
            what: "If non-empty, only these ports are probed.",
            when: "Use when other software owns specific COM ports. Leave empty to probe everything.",
          }}>
          <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
            {(v.include.length ? v.include : [""]).map((p, i) => (
              <div className="shp-listrow" key={i}>
                <input className="shp-input shp-input--mono" defaultValue={p} placeholder="COM3" style={{ maxWidth: 200 }} />
                <Button small variant="ghost">Remove</Button>
              </div>
            ))}
            <Button small variant="ghost" style={{ alignSelf: "flex-start" }}>+ Add row</Button>
          </div>
        </Field>

        <Field label="Exclude list" disabled={v.include.length > 0}
          helpId="cfg-excl" openHelpId={openHelpId}
          helpProps={{
            title: "Exclude list",
            what: "Ports listed here are skipped during probing.",
            when: "Mutually exclusive with Include.",
          }}>
          <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
            {(v.exclude.length ? v.exclude : [""]).map((p, i) => (
              <div className="shp-listrow" key={i}>
                <input className="shp-input shp-input--mono" defaultValue={p}
                  disabled={v.include.length > 0} placeholder="COM7" style={{ maxWidth: 200 }} />
                <Button small variant="ghost" disabled={v.include.length > 0}>Remove</Button>
              </div>
            ))}
            <Button small variant="ghost" style={{ alignSelf: "flex-start" }} disabled={v.include.length > 0}>+ Add row</Button>
            {v.include.length > 0 && (
              <div className="shp-disabled-note">Include and Exclude can't be used together.</div>
            )}
          </div>
        </Field>

        <Field label="Post-open settle"
          helpId="cfg-settle" openHelpId={openHelpId}
          helpProps={{
            title: "Post-open settle (ms)",
            what: "How long to wait after opening a serial port before sending the discovery probe.",
            deflt: "2000 ms",
            when: "Lower if your devices don't reset when the port is opened; raise if discovery is silently missing devices.",
          }}>
          <div className="shp-input-row" style={{ maxWidth: 220 }}>
            <input className="shp-input shp-input--mono" defaultValue={v.settle_ms} />
            <span className="shp-muted">ms</span>
          </div>
        </Field>
      </Section>

      <Section title="Logging"
        helpProps={{ title: "Logging", what: "Verbosity of the service's structured log." }}
        helpId="sec-log" openHelpId={openHelpId}>
        <Field label="Level"
          helpId="cfg-loglvl" openHelpId={openHelpId}
          helpProps={{
            title: "Log level",
            what: "Records at this severity and higher are written to disk.",
            deflt: "info",
            when: "Use debug only while reproducing a problem — debug logs are large.",
          }}>
          <select className="shp-select" style={{ width: 160 }} defaultValue={v.log_level}>
            <option value="debug">debug</option>
            <option value="info">info</option>
            <option value="warn">warn</option>
            <option value="error">error</option>
          </select>
        </Field>
      </Section>

      <Section title="Auto-update"
        helpProps={{ title: "Auto-update", what: "Whether the panel checks for and downloads new SerialHop releases on its own." }}
        helpId="sec-upd" openHelpId={openHelpId}>
        <Field label="Enabled"
          helpId="cfg-upd" openHelpId={openHelpId}
          helpProps={{
            title: "Auto-update",
            what: "On by default. The panel checks once at launch and once per day while open.",
            when: "The actual install still requires an explicit click on the Status tab.",
          }}>
          <Checkbox checked={v.autoupdate} label="Check for updates automatically" />
        </Field>
      </Section>

      <Section title="Raw serial"
        helpProps={{ title: "Raw serial", what: "Exposes a passthrough endpoint that lets clients send/receive arbitrary bytes on any discovered port." }}
        helpId="sec-raw" openHelpId={openHelpId}>
        <Field label="Enabled"
          helpId="cfg-raw" openHelpId={openHelpId}
          helpProps={{
            title: "Raw serial",
            what: "Off by default. Turn on if a client needs byte-level access to a device.",
            when: "Doesn't bypass the firmware-flashing safety — leave that off.",
          }}>
          <Checkbox checked={v.raw_enabled} label="Allow raw passthrough on discovered ports" />
        </Field>
      </Section>

      <Section title="Firmware flashing"
        helpProps={{ title: "Firmware flashing", what: "Higher-risk passthrough that lets clients write firmware to a connected board." }}
        helpId="sec-flash" openHelpId={openHelpId}>
        <div className="shp-info-block">
          <span className="shp-info-block__icon">⚠</span>
          <span>
            Firmware flashing is <b>higher risk</b> than raw serial — a bad firmware file can brick the board,
            requiring physical recovery. Leave disabled unless you're actively flashing devices.
          </span>
        </div>

        <Field label="Enabled"
          helpId="cfg-flash-on" openHelpId={openHelpId}
          helpProps={{
            title: "Firmware flashing enabled",
            what: "Off by default. Turn on only while you're actively flashing.",
            when: "Backups are written to the directory below.",
          }}>
          <Checkbox checked={v.flash_enabled} label="Allow firmware flashing through the service" />
        </Field>

        <Field label="Backup directory" disabled={flashDisabled}
          helpId="cfg-flash-dir" openHelpId={openHelpId}
          helpProps={{
            title: "Firmware backup directory",
            what: "Existing firmware is read off the board and saved here before a new image is flashed.",
            deflt: "%LOCALAPPDATA%/SerialHop/backups",
            when: "Must be an absolute path. Leave empty to use the default.",
          }}>
          <div className="shp-input-row">
            <input className="shp-input shp-input--mono" defaultValue={v.flash_dir}
              disabled={flashDisabled}
              data-error={!!errors.flash_dir} />
            <Button small disabled={flashDisabled}>Choose…</Button>
          </div>
          {errors.flash_dir && <div className="shp-error">{errors.flash_dir}</div>}
        </Field>

        <Field label="Keep N backups" disabled={flashDisabled}
          helpId="cfg-flash-keep" openHelpId={openHelpId}
          helpProps={{
            title: "Keep N backups",
            what: "How many historical backups to retain per device.",
            deflt: "10",
            when: "0 keeps everything forever.",
          }}>
          <input className="shp-input shp-input--mono" defaultValue={v.flash_keep}
            disabled={flashDisabled} style={{ width: 100 }} />
        </Field>
      </Section>

      <div className="shp-btn-row" style={{ marginTop: 14, paddingTop: 4, borderTop: "1px solid var(--border)" }}>
        <Button variant="primary" disabled={!dirty || Object.keys(errors).length > 0}>Save</Button>
        <Button variant="primary" elevated disabled={!dirty || Object.keys(errors).length > 0}>Save & restart</Button>
        <Button disabled={!dirty}>Discard changes</Button>
        <span className="shp-gap" />
        <Button variant="ghost">Open in editor ↗</Button>
      </div>
    </div>
  );
}

Object.assign(window, { ConfigTab });
