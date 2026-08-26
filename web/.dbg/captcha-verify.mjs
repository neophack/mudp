// Visual check of the captcha login flow: rendering, wrong-code rejection +
// auto refresh, click-to-refresh, and a real login through the form.
import { chromium } from "playwright";
const BASE = "http://127.0.0.1:19010";
const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
await page.goto(BASE + "/");
await page.waitForSelector("form.auth-card");
await page.waitForTimeout(500);
await page.screenshot({ path: ".dbg/shots/captcha-login-light.png" });

// dark theme login page
await page.evaluate(() => localStorage.setItem("mudp:theme", "dark"));
await page.reload();
await page.waitForTimeout(500);
await page.screenshot({ path: ".dbg/shots/captcha-login-dark.png" });

// wrong captcha: toast + fresh image + empty input
await page.fill("input[name='username']", "admin");
await page.fill("input[name='password']", "e2e-secret");
await page.fill("input[name='captcha']", "ZZZZZ");
const [toast] = await Promise.all([
  page.waitForSelector(".el-message", { timeout: 8000 }),
  page.click("form.auth-card .auth-submit"),
]);
console.log("wrong-captcha toast:", (await toast.textContent()).trim());
console.log("input cleared after failure:", (await page.inputValue("input[name='captcha']")) === "");

// mobile width login page
const mp = await browser.newPage({ viewport: { width: 390, height: 844 } });
await mp.goto(BASE + "/");
await mp.waitForSelector("form.auth-card");
await mp.waitForTimeout(500);
await mp.screenshot({ path: ".dbg/shots/captcha-login-390.png" });
await mp.close();

// real login through the form (refresh captcha, capture its response)
const [resp] = await Promise.all([
  page.waitForResponse((r) => r.url().includes("/api/captcha")),
  page.click(".captcha-img"),
]);
await page.fill("input[name='captcha']", resp.headers()["x-mudp-captcha-answer"]);
await page.click("form.auth-card .auth-submit");
await page.waitForSelector("aside nav", { timeout: 30000 });
console.log("login-through-form: OK");
await browser.close();
