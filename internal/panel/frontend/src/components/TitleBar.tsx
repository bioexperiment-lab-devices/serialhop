interface TitleBarProps {
  version: string;
}

export function TitleBar({ version }: TitleBarProps) {
  return (
    <div className="shp-titlebar">
      <div className="shp-titlebar__title">
        <b>SerialHop</b> <span className="shp-titlebar__chip">v{version}</span>
      </div>
    </div>
  );
}
