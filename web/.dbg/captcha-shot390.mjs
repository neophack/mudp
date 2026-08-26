import { chromium } from "playwright";
const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 390, height: 844 } });
await page.goto("http://127.0.0.1:19010/");
await page.waitForSelector(".captcha-img img");
await page.waitForTimeout(400);
await page.screenshot({ path: ".dbg/shots/captcha-390-fixed.png" });
await browser.close();
