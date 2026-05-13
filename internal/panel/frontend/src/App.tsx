import { useEffect, useState } from "react";
import { TitleBar } from "./components/TitleBar";
import { TabBar, type TabId } from "./components/TabBar";
import { Warning } from "./components/Warning";
import { Footer } from "./components/Footer";
import { StatusTab } from "./tabs/StatusTab";
import { ConfigTab } from "./tabs/ConfigTab";
import { DevicesTab } from "./tabs/DevicesTab";
import { PortsTab } from "./tabs/PortsTab";
import { LogsTab } from "./tabs/LogsTab";
import { GetVersion, LoadConfigFromDisk } from "./wails/go/main/App";
import { useGlobalUiState } from "./state/globalStore";

export function App() {
  const [version, setVersion] = useState("…");
  const [tab, setTab] = useState<TabId>("status");
  const [configDirty, setConfigDirty] = useState(false);
  const { warn, footer, lamps } = useGlobalUiState();

  useEffect(() => {
    GetVersion().then(setVersion);
    // First-launch: open on Config tab if creds are missing.
    LoadConfigFromDisk().then((cfg: { lab_bridge?: { user?: string; pass?: string } }) => {
      if (!cfg.lab_bridge?.user || !cfg.lab_bridge?.pass) setTab("config");
    });
  }, []);

  return (
    <div className="shp-window">
      <TitleBar version={version} />
      <TabBar active={tab} dirty={configDirty} onChange={setTab} />
      <Warning message={warn} />
      <div className="shp-content">
        <div className="shp-content__pad">
          {tab === "status" && <StatusTab lamps={lamps} />}
          {tab === "config" && <ConfigTab onDirtyChange={setConfigDirty} />}
          {tab === "devices" && <DevicesTab />}
          {tab === "ports" && <PortsTab />}
          {tab === "logs" && <LogsTab />}
        </div>
      </div>
      <Footer {...footer} />
    </div>
  );
}
