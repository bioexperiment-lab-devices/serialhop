import type { ReactNode } from "react";

interface SectionProps {
  title: string;
  helpComponent?: ReactNode;
  children: ReactNode;
}

export function Section({ title, helpComponent, children }: SectionProps) {
  return (
    <section className="shp-form-section">
      <div className="shp-form-section__head">
        {title}
        {helpComponent}
      </div>
      <div className="shp-form-section__body">{children}</div>
    </section>
  );
}
