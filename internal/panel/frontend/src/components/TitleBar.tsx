import type { CSSProperties } from "react";
import { Quit, WindowMinimise } from "../wails/runtime/runtime";

interface TitleBarProps {
  version: string;
}

const dragStyle = { "--wails-draggable": "drag" } as CSSProperties;
const noDragStyle = { "--wails-draggable": "no-drag" } as CSSProperties;

export function TitleBar({ version }: TitleBarProps) {
  return (
    <div className="shp-titlebar">
      <div className="shp-titlebar__drag" style={dragStyle}>
        <div className="shp-titlebar__title">
          <b>SerialHop</b> <span className="shp-titlebar__chip">v{version}</span>
        </div>
      </div>
      <div className="shp-titlebar__buttons" style={noDragStyle}>
        <button
          type="button"
          className="shp-titlebar__btn shp-titlebar__btn--min"
          aria-label="Minimise"
          onClick={() => WindowMinimise()}
        >
          <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
            <rect x="1" y="5" width="8" height="1" fill="currentColor" />
          </svg>
        </button>
        <button
          type="button"
          className="shp-titlebar__btn shp-titlebar__btn--close"
          aria-label="Close"
          onClick={() => Quit()}
        >
          <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
            <path d="M1 1 L9 9 M9 1 L1 9" stroke="currentColor" strokeWidth="1" fill="none" />
          </svg>
        </button>
      </div>
    </div>
  );
}
