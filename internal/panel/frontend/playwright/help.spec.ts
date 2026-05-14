import { test, expect } from "@playwright/test";

test("hover opens popover, mouseout closes after grace", async ({ page }) => {
  await page.goto("/preview.html");
  await page.getByRole("button", { name: "Config" }).click();
  const help = page.locator(".shp-help").first();
  await help.hover();
  await expect(page.getByRole("tooltip")).toBeVisible({ timeout: 500 });
  await page.mouse.move(0, 0);
  await expect(page.getByRole("tooltip")).toBeHidden({ timeout: 500 });
});

test("click makes popover sticky; Esc closes it", async ({ page }) => {
  await page.goto("/preview.html");
  await page.getByRole("button", { name: "Config" }).click();
  const help = page.locator(".shp-help").first();
  await help.click();
  await expect(page.getByRole("tooltip")).toBeVisible();
  await page.mouse.move(0, 0);
  await page.waitForTimeout(300);
  await expect(page.getByRole("tooltip")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("tooltip")).toBeHidden({ timeout: 300 });
});

test("keyboard focus opens; Esc closes", async ({ page }) => {
  await page.goto("/preview.html");
  await page.getByRole("button", { name: "Config" }).click();
  await page.locator(".shp-help").first().focus();
  await expect(page.getByRole("tooltip")).toBeVisible({ timeout: 500 });
  await page.keyboard.press("Escape");
  await expect(page.getByRole("tooltip")).toBeHidden({ timeout: 300 });
});
