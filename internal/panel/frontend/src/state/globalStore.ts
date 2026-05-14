import { useEffect, useState } from "react";
import { EventsOn, EventsOff } from "../wails/runtime/runtime";
import type { ButtonStatePayload, FooterPayload, LampPayload, LampWhich, Tone } from "../types";

interface LampState {
  tone: Tone;
  label: string;
  sub?: string;
}

const DEFAULT_LAMP: LampState = { tone: "grey", label: "Checking…" };
const DEFAULT_BUTTONS: ButtonStatePayload = { install: false, uninstall: false, restart: false };

export function useGlobalUiState() {
  const [warn, setWarn] = useState<string | undefined>();
  const [footer, setFooter] = useState<FooterPayload>({ kind: "info", text: "" });
  const [lamps, setLamps] = useState<Record<LampWhich, LampState>>({
    service: DEFAULT_LAMP,
    server: DEFAULT_LAMP,
    tunnel: DEFAULT_LAMP,
  });
  const [buttons, setButtons] = useState<ButtonStatePayload>(DEFAULT_BUTTONS);

  useEffect(() => {
    const onWarn = (data: { message: string }) => setWarn(data.message);
    const onClear = () => setWarn(undefined);
    const onLamp = (p: LampPayload) =>
      setLamps(prev => ({ ...prev, [p.which]: { tone: p.tone, label: p.label, sub: p.sub } }));
    const onFooter = (p: FooterPayload) => setFooter(p);
    const onButtons = (p: ButtonStatePayload) => setButtons(p);
    EventsOn("warn:set", onWarn);
    EventsOn("warn:clear", onClear);
    EventsOn("status:lamp", onLamp);
    EventsOn("footer:set", onFooter);
    EventsOn("buttons:state", onButtons);
    return () => {
      EventsOff("warn:set");
      EventsOff("warn:clear");
      EventsOff("status:lamp");
      EventsOff("footer:set");
      EventsOff("buttons:state");
    };
  }, []);

  return { warn, footer, lamps, buttons };
}
