type TabId = "status" | "config" | "devices" | "ports" | "cameras" | "logs";

interface TabBarProps {
  active: TabId;
  dirty?: boolean;
  onChange: (id: TabId) => void;
  // hiddenTabs filters tabs out of the rendered bar entirely. Used to
  // gate experimental tabs (e.g. "cameras") behind YAML config flags so
  // users on the stable feature set don't see them.
  hiddenTabs?: TabId[];
}

const TABS: { id: TabId; label: string }[] = [
  { id: "status", label: "Status" },
  { id: "config", label: "Config" },
  { id: "devices", label: "Devices" },
  { id: "ports", label: "Ports" },
  { id: "cameras", label: "Cameras" },
  { id: "logs", label: "Logs" },
];

export function TabBar({ active, dirty, onChange, hiddenTabs }: TabBarProps) {
  const hidden = new Set(hiddenTabs ?? []);
  return (
    <div className="shp-tabs">
      {TABS.filter(t => !hidden.has(t.id)).map(t => (
        <button
          key={t.id}
          className="shp-tab"
          data-active={active === t.id}
          onClick={() => onChange(t.id)}
        >
          {t.label}
          {t.id === "config" && dirty && <span className="shp-tab__dirty" />}
        </button>
      ))}
    </div>
  );
}

export type { TabId };
