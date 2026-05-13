import { EventsOn, EventsOff } from "./wails/runtime/runtime";

/**
 * useWailsEvent — register a Wails event subscription with automatic cleanup.
 *
 * Despite the "use" prefix, this is NOT a React hook. It's a plain helper
 * meant to be called from inside `useEffect`. Returns a cleanup function
 * the effect should return.
 *
 * @example
 *   useEffect(() => useWailsEvent("status:lamp", (p) => setLamp(p)), []);
 */
export function useWailsEvent<T>(name: string, handler: (data: T) => void): () => void {
  const cb = (data: unknown) => handler(data as T);
  EventsOn(name, cb);
  return () => EventsOff(name);
}
