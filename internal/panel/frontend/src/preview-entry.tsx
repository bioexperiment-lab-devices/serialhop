import { createRoot } from "react-dom/client";
import { App as ShimApp } from "./preview-shim/bindings";
import { runtime, startSimulator } from "./preview-shim/events";
import { Scenarios } from "./preview-shim/Scenarios";
import { App } from "./App";
import "./styles/global.css";

// Install Wails-runtime globals BEFORE the SPA's modules execute. The SPA's
// runtime wrapper modules (src/wails/...) call into these globals lazily on
// each invocation, so installation order matters only relative to the first
// call — but earlier is safer.
declare global {
  interface Window {
    go?: { main: { App: typeof ShimApp } };
    runtime?: typeof runtime;
  }
}
window.go = { main: { App: ShimApp } };
window.runtime = runtime;

startSimulator();

const root = createRoot(document.getElementById("root")!);
root.render(
  <>
    <App />
    <Scenarios />
  </>,
);
