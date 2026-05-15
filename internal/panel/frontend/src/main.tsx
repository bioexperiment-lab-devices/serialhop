import React from "react";
import ReactDOM from "react-dom/client";
import { App } from "./App";
import { reportCrash } from "./crashReporter";
import "./styles/global.css";

// Last-resort capture for crashes that escape React's render tree:
// throws inside event handlers (which React already logs but doesn't
// route to ErrorBoundary), unhandled promise rejections from async
// binding calls, etc. Both listeners delegate to reportCrash, which is
// fire-and-forget and never throws.
window.addEventListener("error", (ev) => {
  reportCrash(ev.error ?? ev.message ?? "window.error", "window.error");
});

window.addEventListener("unhandledrejection", (ev) => {
  reportCrash(ev.reason ?? "unhandledrejection", "unhandledrejection");
});

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
