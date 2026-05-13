import { useEffect, useState } from "react";
import { EventsOn, EventsOff } from "../wails/runtime/runtime";
import type { FooterPayload, LampPayload, LampWhich, Tone } from "../types";

interface LampState {
  tone: Tone;
  label: string;
  sub?: string;
}

const DEFAULT_LAMP: LampState = { tone: "grey", label: "Checking…" };

export function useGlobalUiState() {
  const [warn, setWarn] = useState<string | undefined>();
  const [footer, setFooter] = useState<FooterPayload>({ kind: "info", text: "" });
  const [lamps, setLamps] = useState<Record<LampWhich, LampState>>({
    service: DEFAULT_LAMP,
    server: DEFAULT_LAMP,
    tunnel: DEFAULT_LAMP,
  });

  useEffect(() => {
    const onWarn = (data: { message: string }) => setWarn(data.message);
    const onClear = () => setWarn(undefined);
    const onLamp = (p: LampPayload) =>
      setLamps(prev => ({ ...prev, [p.which]: { tone: p.tone, label: p.label, sub: p.sub } }));
    const onFooter = (p: FooterPayload) => setFooter(p);
    EventsOn("warn:set", onWarn);
    EventsOn("warn:clear", onClear);
    EventsOn("status:lamp", onLamp);
    EventsOn("footer:set", onFooter);
    return () => {
      EventsOff("warn:set");
      EventsOff("warn:clear");
      EventsOff("status:lamp");
      EventsOff("footer:set");
    };
  }, []);

  return { warn, footer, lamps };
}
