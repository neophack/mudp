// Third pass: FolderPicker + MCP row dialogs via icon titles.
import { chromium } from "@playwright/test";
import fs from "node:fs";
import path from "node:path";

const BASE = "http://127.0.0.1:19321";
const OUT = "C:/Users/penghongxia/AppData/Local/Temp/mudp-ui-review";
const manifest = [];
let n = 200;

async function shot(page, desc, opts = {}) {
  n += 1;
  const name = `${n}-${opts.slug}.png`;
  await page.waitForTimeout(opts.settle ?? 700);
  await page.screenshot({ path: path.join(OUT, name), fullPage: !!opts.fullPage });
  manifest.push(`${name}\t${desc}`);
  console.log(`[shot] ${name} ${desc}`);
}

async function closeOverlays(page) {
  for (let i = 0; i < 5; i += 1) {
    const vis = page.locator(".el-dialog:visible, .el-message-box:visible, .el-drawer:visible");
    if (!(await vis.count())) return;
    const headerBtn = page.locator(".el-dialog:visible .el-dialog__headerbtn");
    if (i === 0 && (await headerBtn.count())) {
      await headerBtn.first().click();
      await page.waitForTimeout(450);
    } else {
      await page.keyboard.press("Escape");
      await page.waitForTimeout(450);
    }
  }
}

const browser = await chromium.launch();
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: "zh-CN" });
const page = await ctx.newPage();
page.setDefaultTimeout(5000);

const [capRes] = await Promise.all([
  page.waitForResponse((r) => r.url().includes("/api/captcha")),
  page.goto(`${BASE}/login`),
]);
await page.waitForLoadState("domcontentloaded");
await page.locator("button", { hasText: "中文" }).first().click();
await page.waitForTimeout(600);
await page.fill("input[name=username]", "admin");
await page.fill("input[name=password]", "e2e-secret");
await page.fill(".captcha-row input", capRes.headers()["x-mudp-captcha-answer"]);
await page.locator("button.auth-submit").click();
await page.waitForURL("**/dashboard");

// netdisk FolderPicker
await page.goto(`${BASE}/netdisk`);
await page.waitForLoadState("domcontentloaded");
await page.locator(".el-table__row").first().waitFor({ state: "visible", timeout: 8000 });
const copyBtn = page.locator("button", { hasText: /复制 \(/ }).first();
const copyLabel = (await copyBtn.innerText()).trim();
if (copyLabel.includes("(0)")) {
  await page.locator(".el-table__row .el-checkbox").first().click();
  await page.waitForTimeout(400);
}
await copyBtn.click();
await page.locator(".el-dialog:visible").first().waitFor();
await shot(page, "复制目标选择弹窗(FolderPicker)", { slug: "folder-picker", settle: 500 });
const mkdirInPicker = page.locator(".el-dialog:visible button", { hasText: "新建文件夹" });
if (await mkdirInPicker.count()) console.log("[info] FolderPicker has mkdir button");
await closeOverlays(page);

// mcp row dialogs
await page.goto(`${BASE}/mcp`);
await page.waitForLoadState("domcontentloaded");
await page.locator(".row-action-btn").first().waitFor({ state: "visible", timeout: 8000 });
await page.locator('.row-action-btn[title="查看配置"]').first().click();
await page.locator(".el-dialog:visible").first().waitFor();
await shot(page, "MCP 配置弹窗(查看配置)", { slug: "mcp-config-view", settle: 700 });
await closeOverlays(page);
await page.locator('.row-action-btn[title="使用记录"]').first().click();
await page.locator(".el-dialog:visible").first().waitFor();
await shot(page, "MCP 使用记录弹窗", { slug: "mcp-usage", settle: 700 });
await closeOverlays(page);

fs.appendFileSync(path.join(OUT, "manifest.txt"), "\n" + manifest.join("\n"));
console.log("[done] pass 3");
await browser.close();
