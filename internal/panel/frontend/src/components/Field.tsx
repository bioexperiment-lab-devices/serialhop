import type { ReactNode } from "react";

interface FieldProps {
  label: string;
  hint?: string;
  helpComponent?: ReactNode;
  disabled?: boolean;
  children: ReactNode;
}

export function Field({ label, hint, helpComponent, disabled, children }: FieldProps) {
  return (
    <div className="shp-field">
      <label className="shp-field__label" data-disabled={disabled ? true : undefined}>
        <span>{label}</span>
        {helpComponent}
      </label>
      <div className="shp-field__col">
        {children}
        {hint && <div className="shp-field__hint">{hint}</div>}
      </div>
    </div>
  );
}
