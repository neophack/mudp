// Third pass: verify the icon registry change renders every icon correctly.
import { chromium } from "@playwright/test";
import fs from "node:fs";
import path from "node:path";
import { startServer } from "./fixtures/server.js";

const OUT = "C:/Users/penghongxia/AppData/Local/Temp/mudp-opt3-review";
fs.mkdirSync(OUT, { recursive: true });
const server = await startServer({ port: 19397 });
const browser = await chromium.launch();
let n = 0;

async function shot(page, slug, desc, settle = 900) {
  n += 1;
  const name = `${String(n).padStart(2, "0")}-${slug}.png`;
  await page.waitForTimeout(settle);
  await page.screenshot({ path: path.join(OUT, name) });
  console.log(`[shot] ${name} ${desc}`);
}

try {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: "zh-CN" });
  await ctx.addInitScript(() => { if (!localStorage.getItem("mudp_language")) localStorage.setItem("mudp_language", "zh_CN"); });
  const page = await ctx.newPage();
  page.setDefaultTimeout(8000);
  const [cap] = await Promise.all([
    page.waitForResponse((r) => r.url().includes("/api/captcha")),
    page.goto(`${server.url}/login`),
  ]);
  await page.fill("input[name=username]", server.adminUser);
  await page.fill("input[name=password]", server.adminPassword);
  await page.fill("input[name=captcha]", cap.headers()["x-mudp-captcha-answer"]);
  await page.click("form.auth-card .auth-submit");
  await page.waitForURL("**/dashboard");
  await shot(page, "dashboard", "仪表盘:顶栏图标(主题/铃铛/刷新)");
  await page.goto(`${server.url}/netdisk`);
  await page.waitForTimeout(600);
  await shot(page, "netdisk", "网盘:搜索框图标/工具栏");
  await page.goto(`${server.url}/volumes`);
  await page.waitForTimeout(600);
  await shot(page, "volumes", "卷:搜索图标/错误框区域(应不显示)");
  await ctx.close();
} finally {
  await browser.close();
  await server.stop();
}
console.log("done");
