import type { ButtonHTMLAttributes } from "react";

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: "default" | "primary" | "danger" | "ghost";
  elevated?: boolean;
  small?: boolean;
}

export function Button({ variant = "default", elevated, small, children, className, ...rest }: ButtonProps) {
  const cls = [
    "shp-btn",
    variant === "primary" && "shp-btn--primary",
    variant === "danger" && "shp-btn--danger",
    variant === "ghost" && "shp-btn--ghost",
    small && "shp-btn--sm",
    className,
  ].filter(Boolean).join(" ");
  return (
    <button className={cls} {...rest}>
      {elevated && <span className="shp-btn__shield">UAC</span>}
      {children}
    </button>
  );
}
