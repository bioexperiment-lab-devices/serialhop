import { useState } from "react";

interface HelpProps {
  title: string;
  what: string;
  defaultVal?: string;
  when?: string;
}

export function Help({ title, what, defaultVal, when }: HelpProps) {
  const [open, setOpen] = useState(false);
  return (
    <span style={{ position: "relative", display: "inline-flex" }}>
      <span
        className="shp-help"
        data-open={open}
        role="button"
        tabIndex={0}
        onClick={() => setOpen(o => !o)}
        onKeyDown={e => (e.key === "Enter" || e.key === " ") && setOpen(o => !o)}
      >
        ?
      </span>
      {open && (
        <div className="shp-popover" onClick={() => setOpen(false)}>
          <h5>{title}</h5>
          <p>{what}</p>
          {defaultVal && (
            <dl>
              <dt>Default</dt>
              <dd>{defaultVal}</dd>
            </dl>
          )}
          {when && <p style={{ marginTop: 6 }}>{when}</p>}
        </div>
      )}
    </span>
  );
}
