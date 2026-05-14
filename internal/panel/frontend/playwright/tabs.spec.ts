import { test, expect, ConsoleMessage } from "@playwright/test";

const tabs = [
  { id: "status",  label: "Status",  identifier: ".shp-lamps" },
  { id: "config",  label: "Config",  identifier: ".shp-form-section" },
  { id: "devices", label: "Devices", identifier: ".shp-table-wrap, .shp-empty" },
  { id: "ports",   label: "Ports",   identifier: ".shp-table-wrap, .shp-empty" },
  { id: "logs",    label: "Logs",    identifier: ".shp-logs-controls" },
];

test("tab navigation has no console errors and renders identifying element", async ({ page }) => {
  const errors: string[] = [];
  page.on("console", (msg: ConsoleMessage) => {
    if (msg.type() === "error") errors.push(msg.text());
  });
  await page.goto("/preview.html");
  for (const t of tabs) {
    await page.getByRole("button", { name: t.label }).click();
    await expect(page.locator(t.identifier).first()).toBeVisible({ timeout: 2000 });
  }
  expect(errors, errors.join("\n")).toEqual([]);
});
