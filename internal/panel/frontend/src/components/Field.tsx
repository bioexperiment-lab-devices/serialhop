import type { ReactNode } from "react";

interface FieldProps {
  label: string;
  hint?: string;
  helpComponent?: ReactNode;
  disabled?: boolean;
  /** Identifier used by the parent form to scroll/focus this row on error. */
  dataField?: string;
  children: ReactNode;
}

export function Field({ label, hint, helpComponent, disabled, dataField, children }: FieldProps) {
  return (
    <div className="shp-field" data-field={dataField}>
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
