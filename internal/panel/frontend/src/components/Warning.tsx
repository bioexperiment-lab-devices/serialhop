interface WarningProps {
  message?: string;
  tone?: "warn" | "info";
}

export function Warning({ message, tone = "warn" }: WarningProps) {
  if (!message) return null;
  return (
    <div className="shp-warning" data-tone={tone}>
      <span className="shp-warning__icon">&#x26A0;</span>
      <span>{message}</span>
    </div>
  );
}
