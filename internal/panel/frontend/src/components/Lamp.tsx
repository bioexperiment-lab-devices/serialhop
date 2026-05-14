import type { Tone } from "../types";

interface LampProps {
  name: string;
  tone: Tone;
  label: string;
  sub?: string;
  pulse?: boolean;
  children?: React.ReactNode; // for the Help icon
}

export function Lamp({ name, tone, label, sub, pulse, children }: LampProps) {
  return (
    <div className="shp-lamp">
      <div className="shp-lamp__row">
        <span className="shp-lamp__name">{name}</span>
        {children}
      </div>
      <div className="shp-lamp__state">
        <span className="shp-lamp__dot" data-tone={tone} data-pulse={pulse ? true : undefined} />
        <div style={{ display: "flex", flexDirection: "column" }}>
          <span className="shp-lamp__label">{label}</span>
          {sub && <span className="shp-lamp__sub">{sub}</span>}
        </div>
      </div>
    </div>
  );
}
