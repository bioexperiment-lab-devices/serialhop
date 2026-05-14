import { test, expect } from "@playwright/test";

const tabs = [
  { id: "status",  label: "Status",  identifier: ".shp-lamps" },
  { id: "config",  label: "Config",  identifier: ".shp-form-section" },
  { id: "devices", label: "Devices", identifier: ".shp-table-wrap, .shp-empty" },
  { id: "ports",   label: "Ports",   identifier: ".shp-table-wrap, .shp-empty" },
  { id: "logs",    label: "Logs",    identifier: ".shp-logs-controls" },
];

for (const tab of tabs) {
  test(`no horizontal overflow on ${tab.label}`, async ({ page }) => {
    await page.goto("/preview.html");
    await page.getByRole("button", { name: tab.label }).click();
    await expect(page.locator(tab.identifier).first()).toBeVisible({ timeout: 2000 });
    const overflow = await page.evaluate(() => {
      const html = document.documentElement;
      return { scrollW: html.scrollWidth, clientW: html.clientWidth };
    });
    expect(overflow.scrollW).toBeLessThanOrEqual(overflow.clientW + 1);
  });
}
