// Phone-width (390x844) coverage: every page renders without horizontal
// overflow, navigation runs through the drawer, list rows open the bottom
// action sheet, and dialogs clamp to the viewport.
import { test, expect } from "@playwright/test";
import { startServer, seed } from "./fixtures/server.js";
import { installPage, fillCaptcha } from "./fixtures/ui.js";
import fs from "node:fs";
import path from "node:path";

const WIDTH = 390;
const HEIGHT = 844;

test.use({ baseURL: "http://127.0.0.1:19044", hasTouch: true });

let server;

test.beforeAll(async () => {
  server = await startServer({ port: 19044 });
  await seed(server, { runId: "mobile" });
  const root = path.join(server.netdiskRoot, "admin-1");
  fs.mkdirSync(root, { recursive: true });
  fs.writeFileSync(path.join(root, "m-notes.txt"), "mobile\n");
});

test.afterAll(async () => {
  if (server) await server.stop();
});

let helper;

// The desktop login helper waits for the aside nav, which is hidden on
// phones; wait for the mobile header instead.
async function mobileLogin(page, username, password) {
  await page.goto("/");
  await page.locator("form.auth-card").waitFor();
  await page.fill("input[name='username']", username);
  await page.fill("input[name='password']", password);
  await fillCaptcha(page);
  await page.click("form.auth-card .auth-submit");
  await expect(page.locator(".mobile-nav-toggle")).toBeVisible({ timeout: 30000 });
  await page.waitForTimeout(400);
}

// Drawer navigation: hamburger -> nav item -> drawer closes, route swaps.
async function gotoTab(page, tab) {
  await page.locator(".mobile-nav-toggle").click();
  const item = page.locator(".drawer-nav button[data-tab='" + tab + "']");
  await item.waitFor();
  await item.click();
  await expect(page).toHaveURL(new RegExp(tab));
  await page.waitForTimeout(350);
}

function expectNoHorizontalOverflow(page) {
  return expect
    .poll(() =>
      page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth),
      { timeout: 5000 },
    )
    .toBeLessThanOrEqual(1);
}

test.beforeEach(async ({ page }) => {
  // Playwright's default locale is en-US; pin the app to Chinese so the
  // zh UI copy the assertions rely on is actually rendered.
  await page.addInitScript(() => { if (!localStorage.getItem("mudp_language")) localStorage.setItem("mudp_language", "zh_CN"); });
  helper = installPage(page);
  await page.setViewportSize({ width: WIDTH, height: HEIGHT });
  await mobileLogin(page, server.adminUser, server.adminPassword);
});

test.afterEach(async ({ page }) => {
  helper.assertClean("", { ignore503: true });
});

const TABS = [
  "dashboard", "netdisk", "containers", "mcp", "processes", "usage", "images", "volumes",
  "networks", "forwards", "stacks", "hardware", "users", "audit", "security", "errors",
  "disks", "database", "settings", "help",
];

test("every admin page fits the phone viewport without horizontal overflow", async ({ page }) => {
  for (const tab of TABS) {
    await gotoTab(page, tab);
    await page.waitForTimeout(500);
    expect(await page.title()).toBeTruthy();
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(overflow, `${tab} overflows by ${overflow}px`).toBeLessThanOrEqual(1);
  }
});

test("netdisk: row tap opens the bottom action sheet, rename works", async ({ page }) => {
  await gotoTab(page, "netdisk");
  const firstRow = page.locator(".el-table__row").first();
  await firstRow.waitFor();

  // tap the right half of the row: opens the action sheet, not the file
  const box = await firstRow.boundingBox();
  await page.touchscreen.tap(box.x + box.width - 50, box.y + box.height / 2);
  const sheet = page.locator(".el-drawer:visible");
  await expect(sheet).toBeVisible();
  await expect(sheet.locator(".sheet-btn").first()).toBeVisible();

  // rename straight from the sheet
  await sheet.locator(".sheet-btn", { hasText: "重命名" }).click();
  await page.locator(".el-message-box__input input").fill("m-renamed.txt");
  await page.locator(".el-message-box__btns button").last().click();
  await expect(page.locator(".el-table__row", { hasText: "m-renamed.txt" }).first()).toBeVisible({ timeout: 8000 });
});

test("dialogs clamp to the phone viewport", async ({ page }) => {
  await gotoTab(page, "users");
  await page.locator("button", { hasText: "新建用户" }).first().click();
  const dlg = page.locator(".el-dialog:visible").first();
  await dlg.waitFor();
  const box = await dlg.boundingBox();
  expect(box.width).toBeLessThanOrEqual(WIDTH);
  expect(box.x).toBeGreaterThanOrEqual(0);
});

test("plain user sees the phone shell without admin tabs", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: WIDTH, height: HEIGHT }, locale: "zh-CN" });
  const page = await ctx.newPage();
  const h = installPage(page);
  await mobileLogin(page, "e2euser", "e2e-user-secret");

  await page.locator(".mobile-nav-toggle").click();
  await page.locator(".drawer-nav").waitFor();
  await expect(page.locator(".drawer-nav button[data-tab='users']")).toHaveCount(0);
  await expect(page.locator(".drawer-nav button[data-tab='netdisk']")).toBeVisible();
  await page.keyboard.press("Escape");

  await gotoTab(page, "netdisk");
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow).toBeLessThanOrEqual(1);

  h.assertClean("", { ignore503: true });
  await ctx.close();
});
