import { useEffect, useRef, useState, type RefObject } from "react";
import { TitleBar } from "./components/TitleBar";
import { TabBar, type TabId } from "./components/TabBar";
import { Warning } from "./components/Warning";
import { Footer } from "./components/Footer";
import { Modal } from "./components/Modal";
import { Button } from "./components/Button";
import { ErrorBoundary } from "./components/ErrorBoundary";
import { StatusTab } from "./tabs/StatusTab";
import { ConfigTab, type ConfigTabHandle } from "./tabs/ConfigTab";
import { DevicesTab } from "./tabs/DevicesTab";
import { PortsTab } from "./tabs/PortsTab";
import { LogsTab } from "./tabs/LogsTab";
import { GetVersion, LoadConfigFromDisk, TriggerProbe } from "./wails/go/main/App";
import { useGlobalUiState } from "./state/globalStore";

const TAB_LABELS: Record<TabId, string> = {
  status: "Status",
  config: "Config",
  devices: "Devices",
  ports: "Ports",
  logs: "Logs",
};

// TODO(spec §5.10): also intercept window close. Wails v2 exposes
// OnBeforeClose; needs a Go-side bridge to ask the frontend whether
// to allow close. Out of scope for this fix loop.

export function App() {
  const [version, setVersion] = useState("…");
  const [tab, setTab] = useState<TabId>("status");
  const [configDirty, setConfigDirty] = useState(false);
  const [pendingTab, setPendingTab] = useState<TabId | null>(null);
  const { warn, footer, lamps, buttons, logState } = useGlobalUiState();
  const configRef = useRef<ConfigTabHandle | null>(null);

  useEffect(() => {
    GetVersion().then(setVersion);
    // First-launch: open on Config tab if creds are missing.
    LoadConfigFromDisk().then((cfg: { lab_bridge?: { user?: string; pass?: string } }) => {
      if (!cfg.lab_bridge?.user || !cfg.lab_bridge?.pass) setTab("config");
    });
    // Ask the Go side to re-emit network lamp state. The probe goroutines
    // run their initial probe within ~5 s of app startup; if their emit
    // races ahead of the SPA's status:lamp subscription (registered in
    // useGlobalUiState above, which runs before this effect), the lamps
    // would otherwise stay grey "Checking…" until the next 30 s tick.
    // TriggerProbe sets the lamp to Checking…, then re-probes — both
    // events arrive after we've subscribed.
    TriggerProbe("server");
    TriggerProbe("tunnel");
  }, []);

  const requestTab = (next: TabId) => {
    if (configDirty && tab === "config" && next !== "config") {
      setPendingTab(next);
      return;
    }
    setTab(next);
  };

  const onSaveAndSwitch = async () => {
    if (!configRef.current) return;
    const ok = await configRef.current.save();
    if (ok && pendingTab) { setTab(pendingTab); setPendingTab(null); }
  };

  const onDiscardAndSwitch = () => {
    configRef.current?.discard();
    if (pendingTab) { setTab(pendingTab); setPendingTab(null); }
  };

  return (
    <div className="shp-window">
      <TitleBar version={version} />
      <TabBar active={tab} dirty={configDirty} onChange={requestTab} />
      <Warning message={warn} />
      <ErrorBoundary scope="app" version={version}>
        <div className="shp-content">
          <div className="shp-content__pad" data-tab={tab}>
            {tab === "status" && (
              <ErrorBoundary scope="tab:status" version={version}>
                <StatusTab lamps={lamps} buttons={buttons} configDirty={configDirty} />
              </ErrorBoundary>
            )}
            {tab === "config" && (
              <ErrorBoundary scope="tab:config" version={version}>
                <ConfigTab ref={configRef} onDirtyChange={setConfigDirty} />
              </ErrorBoundary>
            )}
            {tab === "devices" && (
              <ErrorBoundary scope="tab:devices" version={version}>
                <DevicesTab />
              </ErrorBoundary>
            )}
            {tab === "ports" && (
              <ErrorBoundary scope="tab:ports" version={version}>
                <PortsTab />
              </ErrorBoundary>
            )}
            {tab === "logs" && (
              <ErrorBoundary scope="tab:logs" version={version}>
                <LogsTab logState={logState} />
              </ErrorBoundary>
            )}
          </div>
        </div>
      </ErrorBoundary>
      <Footer {...footer} />

      {pendingTab && (
        <UnsavedGuardModal
          configRef={configRef}
          nextTab={pendingTab}
          onCancel={() => setPendingTab(null)}
          onDiscard={onDiscardAndSwitch}
          onSave={onSaveAndSwitch}
        />
      )}
    </div>
  );
}

interface UnsavedGuardModalProps {
  configRef: RefObject<ConfigTabHandle | null>;
  nextTab: TabId;
  onCancel: () => void;
  onDiscard: () => void;
  onSave: () => void;
}

function UnsavedGuardModal({ configRef, nextTab, onCancel, onDiscard, onSave }: UnsavedGuardModalProps) {
  const changed = configRef.current?.getChangedFields?.() ?? [];
  const n = changed.length;
  const sub = n > 0
    ? `You've edited ${n} field${n === 1 ? "" : "s"} since the last save.`
    : "You have unsaved configuration changes.";
  return (
    <Modal
      title="Discard unsaved configuration changes?"
      sub={sub}
      actions={
        <>
          <Button variant="ghost" onClick={onCancel}>Cancel</Button>
          <Button variant="danger" onClick={onDiscard}>Discard</Button>
          <Button variant="primary" onClick={onSave}>Save</Button>
        </>
      }
    >
      <p>
        You&apos;re about to switch to the <b>{TAB_LABELS[nextTab]}</b> tab.{" "}
        {n > 0 ? (
          <>
            Your pending edits to {changed.map((label, i) => (
              <span key={label}>
                {i > 0 && (i === changed.length - 1
                  ? (changed.length === 2 ? " and " : ", and ")
                  : ", ")}
                <b>{label}</b>
              </span>
            ))} haven&apos;t been written yet — choose what to do with them before continuing.
          </>
        ) : (
          <>Your pending edits haven&apos;t been written yet — choose what to do with them before continuing.</>
        )}
      </p>
    </Modal>
  );
}
