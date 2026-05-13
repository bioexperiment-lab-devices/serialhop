interface CheckboxProps {
  checked: boolean;
  label: string;
  disabled?: boolean;
  onChange: (next: boolean) => void;
}

export function Checkbox({ checked, label, disabled, onChange }: CheckboxProps) {
  return (
    <span
      className="shp-checkbox"
      data-checked={checked}
      style={{ opacity: disabled ? 0.55 : 1, cursor: disabled ? "default" : "pointer" }}
      onClick={() => !disabled && onChange(!checked)}
    >
      <span className="shp-checkbox__box">{checked ? "✓" : ""}</span>
      <span>{label}</span>
    </span>
  );
}
