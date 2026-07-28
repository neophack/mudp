// Responsive layout smoke test: walks every admin tab (plus a couple of
// representative modals) at phone/tablet/narrow-laptop viewports and fails on
// the classic mobile-layout regressions — content forcing the page to scroll
// sideways, a tab that renders nothing, or a landmark region collapsing to
// zero size. This does not do pixel-level visual diffing; it catches the
// "something is badly broken at this width" class of bug cheaply.
//
// Intentional horizontal-scroll strips (the mobile nav bar, data tables,
// terminal/log output) are NOT flagged here: an element with overflow-x:auto
// clips its own children, so it never inflates document.scrollWidth — the
// check below is exactly the right level for "did the page overflow".

import { test, expect } from "@playwright/test";
import { startServer, seed } from "./fixtures/server.js";
import { ADMIN_TABS, installPage, login, openTab, closeModals } from "./fixtures/ui.js";

const PORT = 19103;
test.use({ baseURL: `http://127.0.0.1:${PORT}` });

test.describe.configure({ mode: "serial" });

let server;
let fixture;

test.beforeAll(async () => {
  server = await startServer({ port: PORT });
  fixture = await seed(server);
});

test.afterAll(async () => {
  if (fixture) await fixture.cleanup();
  if (server) await server.stop();
});

// A small phone, a large phone, a tablet, and a narrow laptop. Desktop widths
// are already exercised by every other spec in this suite at the default
// Playwright viewport.
const VIEWPORTS = [
  { name: "phone-se", width: 320, height: 568 },
  { name: "phone", width: 390, height: 844 },
  { name: "tablet", width: 768, height: 1024 },
  { name: "laptop-narrow", width: 1024, height: 768 },
];

async function assertNoHorizontalOverflow(page, context) {
  const { scrollWidth, clientWidth } = await page.evaluate(() => ({
    scrollWidth: document.documentElement.scrollWidth,
    clientWidth: document.documentElement.clientWidth,
  }));
  expect(scrollWidth, `${context}: page scrolls horizontally (scrollWidth ${scrollWidth} > clientWidth ${clientWidth})`)
    .toBeLessThanOrEqual(clientWidth + 1);
}

for (const vp of VIEWPORTS) {
  test(`every admin tab lays out cleanly at ${vp.name} (${vp.width}x${vp.height})`, async ({ page }) => {
    test.setTimeout(180000);
    await page.setViewportSize({ width: vp.width, height: vp.height });
    const h = installPage(page);
    await login(page, server.adminUser, server.adminPassword);
    await assertNoHorizontalOverflow(page, `login shell @ ${vp.name}`);

    for (const tab of ADMIN_TABS) {
      await openTab(page, tab);
      await assertNoHorizontalOverflow(page, `${tab} tab @ ${vp.name}`);

      // A tab that "renders" an empty or collapsed body reads to a user as a
      // large blank area rather than a real error, so it is easy to miss —
      // assert the content region actually occupies visible space.
      const viewBox = await page.locator("#view").boundingBox();
      expect(viewBox, `${tab} tab @ ${vp.name}: #view has no layout box`).not.toBeNull();
      expect(viewBox.width, `${tab} tab @ ${vp.name}: #view has zero width`).toBeGreaterThan(0);
      expect(viewBox.height, `${tab} tab @ ${vp.name}: #view collapsed to near-zero height`).toBeGreaterThan(20);
    }

    h.assertClean(`the ${vp.name} tab crawl`);
  });
}

// Modals are a common overflow blind spot: a form built for a 640px desktop
// dialog can force horizontal scroll once the viewport itself is narrower
// than the modal's minimum content width.
test("modals stay within the viewport on a phone screen", async ({ page }) => {
  test.setTimeout(60000);
  await page.setViewportSize({ width: 375, height: 812 });
  const h = installPage(page);
  await login(page, server.adminUser, server.adminPassword);

  await openTab(page, "containers");
  await page.click("#newContainerBtn");
  await expect(page.locator(".modal-backdrop").last()).toBeVisible();
  await assertNoHorizontalOverflow(page, "new-container modal @ phone");
  await closeModals(page);

  await openTab(page, "images");
  await page.click("#pullImageBtn");
  await expect(page.locator(".modal-backdrop").last()).toBeVisible();
  await assertNoHorizontalOverflow(page, "pull-image modal @ phone");
  await closeModals(page);

  h.assertClean("the phone-viewport modal check");
});
