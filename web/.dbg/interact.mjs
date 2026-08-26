// Interaction regression: mobile drawer, dialog open/close, sidebar collapse,
// theme toggle button, horizontal overflow check on every page.
import { chromium } from "@playwright/test";

const BASE = "http://127.0.0.1:19010";
const browser = await chromium.launch();
const results = [];
const ok = (name, cond) => results.push((cond ? "PASS " : "FAIL ") + name);

// desktop
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
const page = await ctx.newPage();
await page.goto(BASE + "/");
await page.locator('input[type="text"]').first().fill("admin");
await page.locator('input[type="password"]').first().fill("e2e-secret");
await page.locator("button.auth-submit, form button[type=submit]").first().click();
await page.waitForURL("**/dashboard", { timeout: 10000 });

// every route loads without horizontal overflow at desktop
const routes = ["dashboard","netdisk","containers","mcp","processes","usage","images","volumes","networks","forwards","stacks","hardware","users","audit","security","errors","disks","database","settings","help"];
for (const r of routes) {
  await page.goto(BASE + "/" + r);
  await page.waitForLoadState("domcontentloaded");
  await page.waitForTimeout(400);
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  ok("desktop no-overflow " + r, overflow <= 0);
}

// sidebar collapse
await page.goto(BASE + "/dashboard"); await page.waitForTimeout(400);
await page.locator(".sidebar-toggle").click();
ok("sidebar collapses", await page.locator("aside.shell-aside.collapsed").count() === 1);
await page.locator(".sidebar-toggle").click();
ok("sidebar expands", await page.locator("aside.shell-aside.collapsed").count() === 0);

// theme toggle button in header
const themeBtn = page.locator(".head-actions .icon-btn").first();
await themeBtn.click();
ok("theme toggles to dark", await page.evaluate(() => document.documentElement.dataset.theme) === "dark");
await page.waitForTimeout(300);
await page.screenshot({ path: new URL("./shots/toggled-dark.png", import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, "$1") });
await themeBtn.click();
ok("theme toggles back to light", await page.evaluate(() => document.documentElement.dataset.theme) === "light");

// settings segmented control
await page.goto(BASE + "/settings"); await page.waitForTimeout(500);
const seg = page.locator(".theme-segment");
ok("settings segmented control present", await seg.count() === 1);
await seg.locator("button", { hasText: "深色" }).or(seg.locator("button", { hasText: "Dark" })).first().click();
ok("segmented picks dark", await page.evaluate(() => document.documentElement.dataset.theme) === "dark");

// dialog open/close on containers
await page.goto(BASE + "/containers"); await page.waitForTimeout(500);
const dlgBtn = page.locator("button", { hasText: /创建|Create/ }).first();
if (await dlgBtn.count()) {
  await dlgBtn.click();
  await page.waitForTimeout(500);
  const dlg = page.locator(".el-dialog");
  ok("dialog opens", await dlg.isVisible());
  await page.screenshot({ path: new URL("./shots/dialog-dark.png", import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, "$1") });
  await page.locator(".el-dialog__headerbtn").click();
  await page.waitForTimeout(300);
  ok("dialog closes", !(await page.locator(".el-dialog").isVisible().catch(() => false)));
}

// mobile drawer
const m = await browser.newContext({ viewport: { width: 390, height: 844 } });
const mp = await m.newPage();
await mp.goto(BASE + "/");
await mp.locator('input[type="text"]').first().fill("admin");
await mp.locator('input[type="password"]').first().fill("e2e-secret");
await mp.locator("button.auth-submit, form button[type=submit]").first().click();
await mp.waitForURL("**/dashboard", { timeout: 10000 });
await mp.waitForTimeout(500);
ok("mobile nav toggle visible", await mp.locator(".mobile-nav-toggle").isVisible());
await mp.locator(".mobile-nav-toggle").click();
await mp.waitForTimeout(600);
ok("mobile drawer opens", await mp.locator(".mobile-nav-drawer").isVisible());
await mp.screenshot({ path: new URL("./shots/drawer-dark-390.png", import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, "$1") });
await mp.locator(".drawer-nav .nav-item").nth(2).click();
await mp.waitForTimeout(600);
ok("drawer nav navigates", (await mp.url()).includes("/netdisk") || !(await mp.locator(".mobile-nav-drawer").isVisible()));

// overflow on mobile routes
for (const r of ["dashboard","netdisk","containers","settings","users","images"]) {
  await mp.goto(BASE + "/" + r);
  await mp.waitForTimeout(500);
  const overflow = await mp.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  ok("mobile no-overflow " + r, overflow <= 0);
}

// tablet
const t = await browser.newContext({ viewport: { width: 834, height: 1112 } });
const tp = await t.newPage();
await tp.goto(BASE + "/");
await tp.locator('input[type="text"]').first().fill("admin");
await tp.locator('input[type="password"]').first().fill("e2e-secret");
await tp.locator("button.auth-submit, form button[type=submit]").first().click();
await tp.waitForURL("**/dashboard", { timeout: 10000 });
for (const r of ["dashboard","netdisk","containers","settings"]) {
  await tp.goto(BASE + "/" + r);
  await tp.waitForTimeout(500);
  const overflow = await tp.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  ok("tablet no-overflow " + r, overflow <= 0);
}

await browser.close();
console.log(results.join("\n"));
