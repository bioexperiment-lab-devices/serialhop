import type { FooterKind } from "../types";

interface FooterProps {
  kind?: FooterKind;
  text: string;
  time?: string;
  progress?: number;
}

export function Footer({ kind = "info", text, time, progress }: FooterProps) {
  const kindLabel: Record<FooterKind, string> = {
    ok: "OK",
    work: "···",
    err: "ERR",
    info: "·",
  };
  return (
    <div className="shp-footer">
      <span className="shp-footer__icon" data-kind={kind}>{kindLabel[kind]}</span>
      <span className="shp-footer__text" dangerouslySetInnerHTML={{ __html: text }} />
      {typeof progress === "number" && (
        <span className="shp-footer__progress">
          <i style={{ width: `${progress}%` }} />
        </span>
      )}
      {time && <span className="shp-footer__time">{time}</span>}
    </div>
  );
}
