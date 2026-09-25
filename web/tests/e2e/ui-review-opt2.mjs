// Second optimization pass: verify element-plus on-demand styling across
// tables, dialogs, a message-box, the login card and dark mode.
import { chromium } from "@playwright/test";
import fs from "node:fs";
import path from "node:path";
import { startServer } from "./fixtures/server.js";

const OUT = "C:/Users/penghongxia/AppData/Local/Temp/mudp-opt2-review";
fs.mkdirSync(OUT, { recursive: true });

const server = await startServer({ port: 19395 });
const browser = await chromium.launch();
const manifest = [];
let n = 0;

async function shot(page, slug, desc, opts = {}) {
  n += 1;
  const name = `${String(n).padStart(2, "0")}-${slug}.png`;
  await page.waitForTimeout(opts.settle ?? 900);
  await page.screenshot({ path: path.join(OUT, name), fullPage: !!opts.fullPage });
  manifest.push(`${name}\t${desc}`);
  console.log(`[shot] ${name} ${desc}`);
}

try {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: "zh-CN" });
  await ctx.addInitScript(() => { if (!localStorage.getItem("mudp_language")) localStorage.setItem("mudp_language", "zh_CN"); });
  const page = await ctx.newPage();
  page.setDefaultTimeout(8000);

  // Login card (auth form + captcha row).
  const [capRes] = await Promise.all([
    page.waitForResponse((r) => r.url().includes("/api/captcha")),
    page.goto(`${server.url}/login`),
  ]);
  await page.locator("form.auth-card").waitFor();
  await shot(page, "login", "登录页:卡片/输入框/验证码");
  await page.fill("input[name=username]", server.adminUser);
  await page.fill("input[name=password]", server.adminPassword);
  await page.fill("input[name=captcha]", capRes.headers()["x-mudp-captcha-answer"]);
  await page.click("form.auth-card .auth-submit");
  await page.waitForURL("**/dashboard");

  // Dashboard light.
  await page.waitForSelector(".echart-box canvas", { timeout: 10000 });
  await shot(page, "dashboard-light", "仪表盘:环形图/卡片/表格样式");

  // Settings: switches, selects, form controls.
  await page.goto(`${server.url}/settings`);
  await page.waitForTimeout(800);
  await shot(page, "settings", "设置页:开关/选择器/表单");

  // Netdisk: v-loading table + trigger a delete confirm message-box.
  await page.goto(`${server.url}/netdisk`);
  await page.waitForTimeout(800);
  await page.locator("button", { hasText: "新建文件夹" }).first().click();
  const box = page.locator(".el-message-box:visible");
  await box.waitFor({ timeout: 5000 });
  await shot(page, "netdisk-messagebox", "网盘:新建文件夹消息框");
  await page.keyboard.press("Escape");

  // Dark mode dashboard + settings.
  const darkCtx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: "zh-CN" });
  await darkCtx.addInitScript(() => {
    localStorage.setItem("mudp:theme", "dark");
    if (!localStorage.getItem("mudp_language")) localStorage.setItem("mudp_language", "zh_CN");
  });
  const dpage = await darkCtx.newPage();
  dpage.setDefaultTimeout(8000);
  const [cap2] = await Promise.all([
    dpage.waitForResponse((r) => r.url().includes("/api/captcha")),
    dpage.goto(`${server.url}/login`),
  ]);
  await dpage.locator("form.auth-card").waitFor();
  await shot(dpage, "login-dark", "登录页(暗色)");
  await dpage.fill("input[name=username]", server.adminUser);
  await dpage.fill("input[name=password]", server.adminPassword);
  await dpage.fill("input[name=captcha]", cap2.headers()["x-mudp-captcha-answer"]);
  await dpage.click("form.auth-card .auth-submit");
  await dpage.waitForURL("**/dashboard");
  await dpage.waitForSelector(".echart-box canvas", { timeout: 10000 });
  await shot(dpage, "dashboard-dark", "仪表盘(暗色)");
  await dpage.goto(`${server.url}/settings`);
  await dpage.waitForTimeout(800);
  await shot(dpage, "settings-dark", "设置页(暗色)");
  await darkCtx.close();
  await ctx.close();
} finally {
  fs.writeFileSync(path.join(OUT, "manifest.txt"), manifest.join("\n"));
  await browser.close();
  await server.stop();
}
console.log("done");
