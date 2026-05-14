import { test, expect } from "@playwright/test";

test("every help popover stays inside the viewport", async ({ page }) => {
  await page.goto("/preview.html");
  for (const label of ["Config", "Ports"]) {
    await page.getByRole("button", { name: label }).click();
    await page.waitForTimeout(100);
    const helps = page.locator(".shp-help");
    const count = await helps.count();
    for (let i = 0; i < count; i++) {
      await helps.nth(i).scrollIntoViewIfNeeded();
      await helps.nth(i).hover();
      const tooltip = page.getByRole("tooltip");
      await expect(tooltip).toBeVisible({ timeout: 500 });
      const inside = await tooltip.evaluate((el) => {
        const r = (el as HTMLElement).getBoundingClientRect();
        const vw = window.innerWidth;
        const vh = window.innerHeight;
        return r.left >= 0 && r.top >= 0 && r.right <= vw && r.bottom <= vh;
      });
      expect(inside, `popover #${i} on ${label} clipped`).toBe(true);
      await page.mouse.move(0, 0);
      await expect(tooltip).toBeHidden({ timeout: 500 });
    }
  }
});
