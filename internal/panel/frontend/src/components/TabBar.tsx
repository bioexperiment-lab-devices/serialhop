type TabId = "status" | "config" | "devices" | "ports" | "logs";

interface TabBarProps {
  active: TabId;
  dirty?: boolean;
  onChange: (id: TabId) => void;
}

const TABS: { id: TabId; label: string }[] = [
  { id: "status", label: "Status" },
  { id: "config", label: "Config" },
  { id: "devices", label: "Devices" },
  { id: "ports", label: "Ports" },
  { id: "logs", label: "Logs" },
];

export function TabBar({ active, dirty, onChange }: TabBarProps) {
  return (
    <div className="shp-tabs">
      {TABS.map(t => (
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
