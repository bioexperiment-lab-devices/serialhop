import { useCallback, useEffect, useState } from "react";
import { EventsOn, EventsOff } from "../wails/runtime/runtime";
import { StartLogStream } from "../wails/go/main/App";
import type {
  ButtonStatePayload,
  FooterPayload,
  LampPayload,
  LampWhich,
  LogLinePayload,
  StreamID,
  Tone,
} from "../types";

interface LampState {
  tone: Tone;
  label: string;
  sub?: string;
}

const DEFAULT_LAMP: LampState = { tone: "grey", label: "Checking…" };
const DEFAULT_BUTTONS: ButtonStatePayload = { install: false, uninstall: false, restart: false };

// Bound the in-memory log buffer so a chatty stream can't blow up the
// renderer. Matches the value the LogsTab used previously.
const LOG_RING_CAPACITY = 5_000;

export interface LogStreamState {
  stream: StreamID;
  setStream: (s: StreamID) => void;
  lines: LogLinePayload[];
}

export function useGlobalUiState() {
  const [warn, setWarn] = useState<string | undefined>();
  const [footer, setFooter] = useState<FooterPayload>({ kind: "info", text: "" });
  const [lamps, setLamps] = useState<Record<LampWhich, LampState>>({
    service: DEFAULT_LAMP,
    server: DEFAULT_LAMP,
    tunnel: DEFAULT_LAMP,
  });
  const [buttons, setButtons] = useState<ButtonStatePayload>(DEFAULT_BUTTONS);
  // Log streaming lives at the App level (not inside LogsTab) so the
  // buffer and the tailer subscription both survive tab switches —
  // unmounting LogsTab used to wipe `lines` and call StopLogStream,
  // which was the entire reason logs "disappeared" when switching tabs.
  const [logStream, setLogStream] = useState<StreamID>("service");
  const [logLines, setLogLines] = useState<LogLinePayload[]>([]);

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

  // Log streaming: re-attach the tailer on stream change and pump live
  // lines into `logLines`. Backlog comes back from StartLogStream's
  // return value so it's never lost to an event-registration race.
  useEffect(() => {
    const onLine = (p: LogLinePayload) => {
      if (p.stream !== logStream) return; // ignore stragglers from a previous tail
      setLogLines(prev => {
        const next = prev.length >= LOG_RING_CAPACITY ? prev.slice(prev.length - LOG_RING_CAPACITY + 1) : prev.slice();
        next.push(p);
        return next;
      });
    };
    const onRot = () => setLogLines(prev => [...prev, { stream: logStream, raw: "— rotated —" }]);
    EventsOn("log:line", onLine);
    EventsOn("log:rotated", onRot);

    let cancelled = false;
    setLogLines([]);
    StartLogStream(logStream).then(raw => {
      if (cancelled) return;
      const backlog = (raw ?? []) as LogLinePayload[];
      if (backlog.length === 0) return;
      setLogLines(prev => [...backlog, ...prev]);
    });

    return () => {
      cancelled = true;
      EventsOff("log:line");
      EventsOff("log:rotated");
    };
  }, [logStream]);

  const setStream = useCallback((next: StreamID) => setLogStream(next), []);
  const logState: LogStreamState = { stream: logStream, setStream, lines: logLines };

  return { warn, footer, lamps, buttons, logState };
}
