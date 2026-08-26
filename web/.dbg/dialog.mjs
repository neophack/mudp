import { chromium } from "@playwright/test";
const BASE = "http://127.0.0.1:19010";
const browser = await chromium.launch();
const page = await (await browser.newContext({ viewport: { width: 1440, height: 900 } })).newPage();
await page.goto(BASE + "/");
await page.locator('input[type="text"]').first().fill("admin");
await page.locator('input[type="password"]').first().fill("e2e-secret");
await page.locator("button.auth-submit, form button[type=submit]").first().click();
await page.waitForURL("**/dashboard", { timeout: 10000 });
await page.evaluate(() => localStorage.setItem("mudp:theme", "dark"));
await page.goto(BASE + "/settings");
await page.waitForTimeout(600);
const add = page.locator("button", { hasText: /添加|Add/ }).first();
console.log("add-button count:", await add.count());
if (await add.count()) {
  await add.click();
  await page.waitForTimeout(500);
  console.log("dialog visible:", await page.locator(".el-dialog").isVisible());
  await page.screenshot({ path: ".dbg/shots/dialog-dark.png" });
  await page.locator(".el-dialog__headerbtn").click();
  await page.waitForTimeout(300);
  console.log("dialog closed:", (await page.locator(".el-dialog").count()) === 0);
}
await browser.close();
