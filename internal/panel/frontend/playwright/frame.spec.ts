import { test, expect } from "@playwright/test";

test("no faux window border or border-radius on .shp-window", async ({ page }) => {
  await page.goto("/preview.html");
  await page.waitForSelector(".shp-window");
  const computed = await page.evaluate(() => {
    const w = document.querySelector(".shp-window")!;
    const s = getComputedStyle(w as Element);
    return { borderTop: s.borderTopWidth, borderLeft: s.borderLeftWidth, borderRadius: s.borderTopLeftRadius };
  });
  expect(computed.borderTop).toBe("0px");
  expect(computed.borderLeft).toBe("0px");
  expect(computed.borderRadius).toBe("0px");
});
