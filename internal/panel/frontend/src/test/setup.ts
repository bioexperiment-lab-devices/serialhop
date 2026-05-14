import "@testing-library/jest-dom";
import { beforeEach, afterEach } from "vitest";
import { cleanup } from "@testing-library/react";

beforeEach(() => {
  if (!document.getElementById("popover-root")) {
    const root = document.createElement("div");
    root.id = "popover-root";
    document.body.appendChild(root);
  }
});

afterEach(() => {
  // Run React cleanup first so portals unmount before we clear the host node.
  cleanup();
  document.getElementById("popover-root")?.replaceChildren();
});
