import { test, expect } from "@playwright/test";

const tabs = [
  { id: "status",  label: "Status" },
  { id: "config",  label: "Config" },
  { id: "devices", label: "Devices" },
  { id: "ports",   label: "Ports" },
  { id: "logs",    label: "Logs" },
];

for (const tab of tabs) {
  test(`no horizontal overflow on ${tab.label}`, async ({ page }) => {
    await page.goto("/preview.html");
    await page.getByRole("button", { name: tab.label }).click();
    await page.waitForTimeout(150);
    const overflow = await page.evaluate(() => {
      const html = document.documentElement;
      return { scrollW: html.scrollWidth, clientW: html.clientWidth };
    });
    expect(overflow.scrollW).toBeLessThanOrEqual(overflow.clientW + 1);
  });
}
