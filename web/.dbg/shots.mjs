// Visual verification harness: login once, walk key pages, screenshot each at
// desktop / tablet / phone widths in both light and dark themes.
import { chromium } from "@playwright/test";

const BASE = "http://127.0.0.1:19010";
const OUT = new URL("./shots/", import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, "$1");

const PAGES = [
  ["dashboard", "仪表盘"],
  ["netdisk", "网盘"],
  ["containers", "容器"],
  ["settings", "设置"],
];

const browser = await chromium.launch();
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
const page = await ctx.newPage();
await page.goto(BASE + "/");
await page.waitForLoadState("domcontentloaded");
// login
await page.locator('input[type="text"]').first().fill("admin");
await page.locator('input[type="password"]').first().fill("e2e-secret");
await page.locator("button.auth-submit, form button[type=submit]").first().click();
await page.waitForURL("**/dashboard", { timeout: 10000 });

for (const [route] of PAGES) {
  await page.goto(BASE + "/" + route);
  await page.waitForLoadState("domcontentloaded");
  await page.waitForTimeout(600);
  await page.screenshot({ path: OUT + route + "-light-1440.png" });
}
// dark
await page.evaluate(() => localStorage.setItem("mudp:theme", "dark"));
await page.reload();
await page.waitForTimeout(600);
for (const [route] of PAGES) {
  await page.goto(BASE + "/" + route);
  await page.waitForLoadState("domcontentloaded");
  await page.waitForTimeout(600);
  await page.screenshot({ path: OUT + route + "-dark-1440.png" });
}
// phone + tablet, dark
for (const size of [[390, 844, "390"], [834, 1112, "834"]]) {
  const c2 = await browser.newContext({ viewport: { width: size[0], height: size[1] } });
  const p2 = await c2.newPage();
  await p2.goto(BASE + "/");
  await p2.locator('input[type="text"]').first().fill("admin");
  await p2.locator('input[type="password"]').first().fill("e2e-secret");
  await p2.locator("button.auth-submit, form button[type=submit]").first().click();
  await p2.waitForURL("**/dashboard", { timeout: 10000 });
  await p2.evaluate(() => localStorage.setItem("mudp:theme", "dark"));
  await p2.reload();
  await p2.waitForTimeout(600);
  for (const [route] of [["dashboard", "仪表盘"], ["netdisk", "网盘"], ["containers", "容器"], ["settings", "设置"]]) {
    await p2.goto(BASE + "/" + route);
    await p2.waitForLoadState("domcontentloaded");
    await p2.waitForTimeout(600);
    await p2.screenshot({ path: OUT + route + `-dark-${size[2]}.png` });
  }
  await c2.close();
}
await browser.close();
console.log("DONE");
