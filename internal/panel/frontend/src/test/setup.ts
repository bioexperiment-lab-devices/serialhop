import "@testing-library/jest-dom";
import { beforeEach, afterEach } from "vitest";

beforeEach(() => {
  if (!document.getElementById("popover-root")) {
    const root = document.createElement("div");
    root.id = "popover-root";
    document.body.appendChild(root);
  }
});

afterEach(() => {
  document.getElementById("popover-root")?.replaceChildren();
});
