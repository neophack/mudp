// Fifth pass: real upload -> UploadOverlay; batch copy -> CopyOverlay.
import { chromium } from "@playwright/test";
import fs from "node:fs";
import path from "node:path";

const BASE = "http://127.0.0.1:19321";
const OUT = "C:/Users/penghongxia/AppData/Local/Temp/mudp-ui-review";
const manifest = [];
let n = 400;

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
page.setDefaultTimeout(6000);

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

// upload two files through the hidden file input on the netdisk page
await page.goto(`${BASE}/netdisk`);
await page.waitForLoadState("domcontentloaded");
await page.locator(".el-table__row").first().waitFor({ state: "visible", timeout: 8000 });
const fileInputs = page.locator('input[type="file"]');
const inputCount = await fileInputs.count();
console.log(`[info] file inputs on page: ${inputCount}`);
const tmp1 = path.join(OUT, "upload-a.png");
const tmp2 = path.join(OUT, "upload-b.txt");
fs.copyFileSync("D:/mudp/web/dist/mudp.png", tmp1);
fs.writeFileSync(tmp2, "upload overlay smoke\n".repeat(200));
await fileInputs.first().setInputFiles([tmp1, tmp2]);
await page.waitForTimeout(500);
await shot(page, "上传浮层(UploadOverlay,进行中/完成)", { slug: "upload-overlay", settle: 200 });
await page.waitForTimeout(2500);
await closeOverlays(page);

// batch copy -> FolderPicker -> confirm -> CopyOverlay
await page.locator(".el-table__row .el-checkbox").nth(2).click();
await page.waitForTimeout(400);
await page.locator("button", { hasText: /复制 \(/ }).first().click();
await page.locator(".el-dialog:visible").first().waitFor();
await page.locator(".el-dialog:visible button", { hasText: "复制" }).last().click();
await page.waitForTimeout(600);
await shot(page, "复制/移动进度浮层(CopyOverlay)", { slug: "copy-overlay", settle: 300 });
await page.waitForTimeout(1500);

// jobs panel afterwards (should list the finished task)
await page.goto(`${BASE}/dashboard`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(800);
await page.locator(".head-actions .el-badge").first().click();
await page.locator(".el-dialog:visible").first().waitFor();
await shot(page, "后台任务面板(有历史任务)", { slug: "jobs-panel-tasks", settle: 600 });
await closeOverlays(page);

fs.appendFileSync(path.join(OUT, "manifest.txt"), "\n" + manifest.join("\n"));
console.log("[done] pass 5");
await browser.close();
