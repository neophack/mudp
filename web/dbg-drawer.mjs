// Debug the mobile nav drawer: open it at 768 and 375, dump state after each click.
import { chromium } from "playwright";

const BASE = process.argv[2] || "http://127.0.0.1:19200";
const browser = await chromium.launch();

for (const width of [768, 375]) {
  const page = await browser.newPage({ viewport: { width, height: 1024 } });
  const errors = [];
  page.on("pageerror", (e) => errors.push(`${width}: ${e.message}`));
  page.on("console", (m) => { if (m.type() === "error") errors.push(`${width} console: ${m.text()}`); });
  await page.goto(BASE + "/");
  await page.fill("input[name='username']", "admin");
  await page.fill("input[name='password']", "smoke-secret-123");
  await page.click("form.auth-card .auth-submit");
  await page.waitForSelector(".work-header", { timeout: 60000 });

  const toggle = page.locator(".work-header .mobile-nav-toggle");
  console.log(`\n=== width ${width} ===`);
  console.log("toggle count:", await toggle.count(), "visible:", await toggle.isVisible().catch(() => "-"));

  await toggle.click({ timeout: 5000 }).then(() => console.log("toggle click ok"), (e) => console.log("toggle click FAIL:", e.message.split("\n")[0]));
  await page.waitForTimeout(800);
  const drawerWrapper = page.locator(".el-drawer__wrapper");
  console.log("drawer wrapper count:", await drawerWrapper.count());
  if (await drawerWrapper.count()) {
    console.log("drawer display:", await drawerWrapper.evaluate((el) => getComputedStyle(el).display));
    console.log("drawer nav items:", await page.locator(".el-drawer__wrapper nav button, .drawer-nav button").count());
  }
  // Now click a nav item inside the drawer
  const btn = page.locator(".el-drawer__wrapper .drawer-nav button[data-tab='containers']").first();
  console.log("drawer nav btn count:", await btn.count(), "visible:", await btn.isVisible().catch(() => "-"));
  if (await btn.count()) {
    await btn.click({ timeout: 5000 }).then(() => console.log("nav click ok"), (e) => console.log("nav click FAIL:", e.message.split("\n")[0]));
    await page.waitForTimeout(600);
    console.log("url after click:", page.url());
    console.log("drawer display after nav:", await drawerWrapper.evaluate((el) => getComputedStyle(el).display).catch(() => "-"));
  }
  await page.screenshot({ path: `test-results/dbg-drawer-${width}.png` });
  await page.close();
  console.log("errors:", errors.length ? errors : "none");
}
await browser.close();
