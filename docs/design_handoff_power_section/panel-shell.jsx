// SerialHop panel — shell + shared primitives
/* global React */

const { useState } = React;

const TABS = [
  { id: "status",  label: "Status"  },
  { id: "config",  label: "Config"  },
  { id: "devices", label: "Devices" },
  { id: "ports",   label: "Ports"   },
  { id: "logs",    label: "Logs"    },
];

function Help({ id, openHelpId, title, what, deflt, when }) {
  const open = openHelpId === id;
  return (
    <span style={{ position: "relative", display: "inline-flex" }}>
      <span className="shp-help" data-open={open}>?</span>
      {open && (
        <div className="shp-popover">
          <h5>{title}</h5>
          <p>{what}</p>
          {deflt && (
            <dl>
              <dt>Default</dt><dd>{deflt}</dd>
            </dl>
          )}
          {when && <p style={{ marginTop: 6 }}>{when}</p>}
        </div>
      )}
    </span>
  );
}

function Footer({ kind = "info", text, time, progress }) {
  const kindLabel = {
    ok:   "OK",
    work: "···",
    err:  "ERR",
    info: "·",
  }[kind] || "·";
  return (
    <div className="shp-footer">
      <span className="shp-footer__icon" data-kind={kind}>{kindLabel}</span>
      <span className="shp-footer__text" dangerouslySetInnerHTML={{ __html: text }} />
      {typeof progress === "number" && (
        <span className="shp-footer__progress"><i style={{ width: `${progress}%` }} /></span>
      )}
      {time && <span className="shp-footer__time">{time}</span>}
    </div>
  );
}

function TitleBar({ version }) {
  return (
    <div className="shp-titlebar">
      <div className="shp-titlebar__title">
        <b>SerialHop</b> <span className="shp-titlebar__chip">v{version}</span>
      </div>
      <div className="shp-titlebar__buttons">
        <span className="shp-titlebar__btn">—</span>
        <span className="shp-titlebar__btn">▢</span>
        <span className="shp-titlebar__btn">✕</span>
      </div>
    </div>
  );
}

function TabBar({ tabs, active, dirty }) {
  return (
    <div className="shp-tabs">
      {tabs.map(t => (
        <button key={t.id} className="shp-tab" data-active={active === t.id}>
          {t.label}
          {t.id === "config" && dirty && <span className="shp-tab__dirty" />}
        </button>
      ))}
    </div>
  );
}

function Warning({ message, tone = "warn" }) {
  if (!message) return null;
  return (
    <div className="shp-warning" data-tone={tone}>
      <span className="shp-warning__icon">⚠</span>
      <span>{message}</span>
    </div>
  );
}

function Lamp({ name, tone, label, sub, pulse, helpId, openHelpId, helpProps }) {
  return (
    <div className="shp-lamp">
      <div className="shp-lamp__row">
        <span className="shp-lamp__name">{name}</span>
        <Help id={helpId} openHelpId={openHelpId} {...helpProps} />
      </div>
      <div className="shp-lamp__state">
        <span className="shp-lamp__dot" data-tone={tone} data-pulse={pulse}></span>
        <div style={{ display: "flex", flexDirection: "column" }}>
          <span className="shp-lamp__label">{label}</span>
          {sub && <span className="shp-lamp__sub">{sub}</span>}
        </div>
      </div>
    </div>
  );
}

function Button({ variant = "default", elevated, disabled, children, small }) {
  const cls = [
    "shp-btn",
    variant === "primary" && "shp-btn--primary",
    variant === "danger"  && "shp-btn--danger",
    variant === "ghost"   && "shp-btn--ghost",
    small && "shp-btn--sm",
  ].filter(Boolean).join(" ");
  return (
    <button className={cls} disabled={disabled}>
      {elevated && <span className="shp-btn__shield">UAC</span>}
      {children}
    </button>
  );
}

function Checkbox({ checked, label, disabled }) {
  return (
    <span className="shp-checkbox" data-checked={checked} style={{ opacity: disabled ? 0.55 : 1 }}>
      <span className="shp-checkbox__box">{checked ? "✓" : ""}</span>
      <span>{label}</span>
    </span>
  );
}

function Field({ label, hint, helpId, openHelpId, helpProps, disabled, children }) {
  return (
    <div className="shp-field">
      <label className="shp-field__label" data-disabled={disabled}>
        <span>{label}</span>
        {helpProps && <Help id={helpId} openHelpId={openHelpId} {...helpProps} />}
      </label>
      <div className="shp-field__col">
        {children}
        {hint && <div className="shp-field__hint">{hint}</div>}
      </div>
    </div>
  );
}

function Section({ title, helpProps, helpId, openHelpId, children }) {
  return (
    <section className="shp-form-section">
      <div className="shp-form-section__head">
        {title}
        {helpProps && <Help id={helpId} openHelpId={openHelpId} {...helpProps} />}
      </div>
      <div className="shp-form-section__body">{children}</div>
    </section>
  );
}

function Modal({ title, sub, children, actions }) {
  return (
    <div className="shp-modal-scrim">
      <div className="shp-modal">
        <div className="shp-modal__head">
          <h3 className="shp-modal__title">{title}</h3>
          {sub && <div className="shp-modal__sub">{sub}</div>}
        </div>
        <div className="shp-modal__body">{children}</div>
        <div className="shp-modal__foot">{actions}</div>
      </div>
    </div>
  );
}

// Window shell that wraps any tab body
function PanelWindow({ version, warning, activeTab, dirty, footer, children, modal }) {
  return (
    <div className="shp-window">
      <TitleBar version={version} />
      <TabBar tabs={TABS} active={activeTab} dirty={dirty} />
      <Warning message={warning} />
      <div className="shp-content">
        <div className="shp-content__pad">{children}</div>
      </div>
      <Footer {...footer} />
      {modal}
    </div>
  );
}

Object.assign(window, {
  PanelWindow, Footer, TitleBar, TabBar, Warning, Lamp,
  Button, Checkbox, Field, Section, Help, Modal,
});
