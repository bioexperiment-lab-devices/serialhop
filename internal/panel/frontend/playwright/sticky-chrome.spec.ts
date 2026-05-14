import { test, expect } from "@playwright/test";

// The chrome (titlebar + tabs + warning + footer) must stay pinned on long
// pages — only `.shp-content` should scroll. Achieved by `.shp-window` using
// `height: 100vh` (not `min-height`), which clamps the chrome and forces the
// inner content to be the only scroll container.
//
// Test injects a tall sentinel into the content pad so the assertion holds
// regardless of how much real content the page renders.

test("only .shp-content scrolls; document and chrome stay pinned", async ({ page }) => {
  await page.goto("/preview.html");
  await page.getByRole("button", { name: "Config" }).click();
  await expect(page.locator(".shp-form-section").first()).toBeVisible();

  await page.locator(".shp-content__pad").evaluate((el) => {
    const sentinel = document.createElement("div");
    sentinel.style.height = "3000px";
    sentinel.setAttribute("data-test-sentinel", "true");
    el.appendChild(sentinel);
  });

  const tabsTopBefore = await page.locator(".shp-tabs").evaluate((el) => el.getBoundingClientRect().top);

  // Try to scroll both the document and the inner content; only the latter should move.
  await page.evaluate(() => window.scrollTo(0, 9999));
  await page.locator(".shp-content").evaluate((el) => { el.scrollTop = 9999; });

  const result = await page.evaluate(() => ({
    docOverflow: document.documentElement.scrollHeight - document.documentElement.clientHeight,
    windowScrollY: window.scrollY,
    contentScrollTop: (document.querySelector(".shp-content") as HTMLElement).scrollTop,
  }));
  const tabsTopAfter = await page.locator(".shp-tabs").evaluate((el) => el.getBoundingClientRect().top);

  expect(result.docOverflow).toBe(0);
  expect(result.windowScrollY).toBe(0);
  expect(result.contentScrollTop).toBeGreaterThan(0);
  expect(tabsTopAfter).toBe(tabsTopBefore);
});
