// Second pass: re-shoot everything the first pass skipped (netdisk now has
// per-user files; ElMessageBox buttons are 取消/确认; dialogs filtered :visible;
// sidebar logout is 登出).
import { chromium } from "@playwright/test";
import fs from "node:fs";
import path from "node:path";

const BASE = "http://127.0.0.1:19321";
const OUT = "C:/Users/penghongxia/AppData/Local/Temp/mudp-ui-review";
const manifest = [];
let n = 100;

async function shot(page, desc, opts = {}) {
  n += 1;
  const name = `${n}-${opts.slug}.png`;
  await page.waitForTimeout(opts.settle ?? 700);
  await page.screenshot({ path: path.join(OUT, name), fullPage: !!opts.fullPage });
  manifest.push(`${name}\t${desc}`);
  console.log(`[shot] ${name} ${desc}`);
}

async function tryStep(desc, fn) {
  try {
    await fn();
  } catch (err) {
    console.log(`[SKIP] ${desc}: ${err.message.split("\n")[0]}`);
  }
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

async function openDialog(page, trigger) {
  await trigger();
  await page.locator(".el-dialog:visible").first().waitFor({ state: "visible" });
}

const browser = await chromium.launch();
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: "zh-CN" });
const page = await ctx.newPage();
page.setDefaultTimeout(5000);

// login (switch to Chinese first)
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
console.log("[ok] logged in as admin");

// ---------- netdisk with files ----------
await page.goto(`${BASE}/netdisk`);
await page.waitForLoadState("domcontentloaded");
await page.locator(".el-table__row").first().waitFor({ state: "visible", timeout: 8000 });
await shot(page, "网盘(有文件)", { slug: "netdisk-files", fullPage: true });

await tryStep("新建文件夹确认", async () => {
  await page.locator("button", { hasText: "新建文件夹" }).first().click();
  await page.locator(".el-message-box:visible").waitFor();
  await page.locator(".el-message-box__input input").fill("走查文件夹");
  await shot(page, "新建文件夹输入框(已填写)", { slug: "netdisk-mkdir2", settle: 400 });
  await page.locator(".el-message-box__btns button").last().click();
  await page.waitForTimeout(1000);
  await shot(page, "新建文件夹成功后的列表", { slug: "netdisk-mkdir-done", settle: 500 });
});

await tryStep("批量条+分享两步", async () => {
  await page.locator(".el-table__row .el-checkbox").first().click();
  await page.waitForTimeout(400);
  await shot(page, "网盘批量操作条(选中 1 项)", { slug: "netdisk-batch" });
  await page.locator("button", { hasText: "分享 (" }).first().click();
  await page.locator(".el-dialog:visible").first().waitFor();
  await shot(page, "分享弹窗-表单步", { slug: "share-form", settle: 500 });
  await page.locator(".el-dialog:visible button", { hasText: "创建链接" }).last().click();
  await page.waitForTimeout(1400);
  await shot(page, "分享弹窗-结果步(链接已生成)", { slug: "share-result", fullPage: true });
  await closeOverlays(page);
});

await tryStep("FolderPicker(复制)", async () => {
  await page.locator(".el-table__row .el-checkbox").first().click();
  await page.waitForTimeout(300);
  await page.locator("button", { hasText: "复制 (" }).first().click();
  await page.locator(".el-dialog:visible").first().waitFor();
  await shot(page, "复制目标选择弹窗(FolderPicker)", { slug: "folder-picker", settle: 500 });
  await closeOverlays(page);
});

await tryStep("文本预览", async () => {
  await page.locator(".name-link", { hasText: "说明文档.txt" }).first().click();
  await page.waitForTimeout(1400);
  await shot(page, "文件预览-文本", { slug: "viewer-text" });
  await closeOverlays(page);
});
await tryStep("图片预览", async () => {
  await page.locator(".name-link", { hasText: "logo.png" }).first().click();
  await page.waitForTimeout(1800);
  await shot(page, "文件预览-图片", { slug: "viewer-image" });
  await closeOverlays(page);
});
await tryStep("行内操作图标(重命名 prompt)", async () => {
  const btns = page.locator(".el-table__row").first().locator(".row-action-btn");
  const cnt = await btns.count();
  console.log(`[info] row action icons: ${cnt}`);
  const rename = page.locator('.el-table__row .row-action-btn[title="重命名"]');
  if (await rename.count()) {
    await rename.first().click();
    await page.locator(".el-message-box:visible").waitFor();
    await shot(page, "行内重命名输入框", { slug: "netdisk-rename", settle: 400 });
    await page.keyboard.press("Escape");
    await page.waitForTimeout(400);
  } else {
    console.log("[SKIP] rename icon not found");
  }
});

// ---------- mcp usage ----------
await page.goto(`${BASE}/mcp`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(1000);
await tryStep("MCP 使用记录", async () => {
  await page.locator("button", { hasText: "使用记录" }).first().click();
  await page.locator(".el-dialog:visible").first().waitFor();
  await shot(page, "MCP 使用记录弹窗", { slug: "mcp-usage", settle: 600 });
  await closeOverlays(page);
});
await tryStep("MCP 配置弹窗(查看配置)", async () => {
  await page.locator("button", { hasText: "查看配置" }).first().click();
  await page.locator(".el-dialog:visible").first().waitFor();
  await shot(page, "MCP 配置弹窗(查看配置)", { slug: "mcp-config2", settle: 600 });
  await closeOverlays(page);
});

// ---------- images remaining dialogs ----------
await page.goto(`${BASE}/images`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(900);
await tryStep("导入镜像弹窗", async () => {
  await openDialog(page, () => page.locator("button", { hasText: "导入" }).first().click());
  await shot(page, "导入镜像弹窗", { slug: "image-import", settle: 500 });
  await closeOverlays(page);
});
await tryStep("注册镜像弹窗", async () => {
  await openDialog(page, () => page.locator("button", { hasText: "注册" }).first().click());
  await shot(page, "注册镜像弹窗", { slug: "image-register", settle: 500 });
  await closeOverlays(page);
});
await tryStep("拉取镜像弹窗", async () => {
  await openDialog(page, () => page.locator("button", { hasText: "拉取镜像" }).first().click());
  await shot(page, "拉取镜像弹窗", { slug: "image-pull", settle: 500 });
  await closeOverlays(page);
});

// ---------- users dialogs ----------
await page.goto(`${BASE}/users`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(1000);
await tryStep("用户编辑弹窗", async () => {
  await page.locator(".el-table__row", { hasText: "e2euser" }).locator("button", { hasText: "编辑" }).first().click();
  await page.locator(".el-dialog:visible").first().waitFor();
  await shot(page, "用户编辑弹窗(e2euser)", { slug: "user-edit", settle: 600 });
  await closeOverlays(page);
});
await tryStep("用户组编辑弹窗", async () => {
  await page.locator(".el-table__row", { hasText: "e2euser" }).locator("button", { hasText: "用户组" }).first().click();
  await page.locator(".el-dialog:visible").first().waitFor();
  await shot(page, "编辑用户组弹窗(e2euser)", { slug: "user-groups", settle: 600 });
  await closeOverlays(page);
});
await tryStep("用户组设置弹窗", async () => {
  await page.locator(".el-table__row", { hasText: "users" }).locator("button", { hasText: "编辑" }).last().click();
  await page.locator(".el-dialog:visible").first().waitFor();
  await shot(page, "用户组设置弹窗(users)", { slug: "group-settings", settle: 600 });
  await closeOverlays(page);
});
await tryStep("新建用户组 prompt", async () => {
  await page.locator("button", { hasText: "新建用户组" }).first().click();
  await page.locator(".el-message-box:visible").waitFor();
  await shot(page, "新建用户组输入框", { slug: "group-new", settle: 400 });
  await page.locator(".el-message-box__input input").fill("走查组");
  await page.locator(".el-message-box__btns button").last().click();
  await page.waitForTimeout(900);
});

// ---------- global overlays ----------
await page.goto(`${BASE}/dashboard`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(800);
await tryStep("通知面板", async () => {
  await page.locator(".head-actions .el-badge").nth(1).click();
  await page.locator(".el-dialog:visible").first().waitFor();
  await shot(page, "通知面板", { slug: "notif-panel", settle: 600 });
  await closeOverlays(page);
});
await tryStep("暗色主题", async () => {
  await page.locator(".head-actions button[title*='外观']").first().click();
  await shot(page, "仪表盘-暗色主题", { slug: "dashboard-dark", settle: 800, fullPage: true });
  await page.locator(".head-actions button[title*='外观']").first().click();
  await page.waitForTimeout(500);
});
await tryStep("侧栏折叠态", async () => {
  await page.locator('button[aria-label="Toggle sidebar"]').click();
  await shot(page, "侧栏折叠态", { slug: "sidebar-collapsed", settle: 500 });
  await page.locator('button[aria-label="Toggle sidebar"]').click();
  await page.waitForTimeout(400);
});

// ---------- mobile netdisk with files ----------
const mpage = await ctx.newPage();
await mpage.setViewportSize({ width: 390, height: 844 });
mpage.setDefaultTimeout(5000);
await mpage.goto(`${BASE}/netdisk`);
await mpage.waitForLoadState("domcontentloaded");
await mpage.locator(".el-table__row").first().waitFor({ state: "visible", timeout: 8000 });
await mpage.waitForTimeout(600);
await shot(mpage, "手机-网盘(有文件)", { slug: "m-netdisk2" });
await tryStep("手机 ActionSheet", async () => {
  await mpage.locator(".el-table__row").first().click({ position: { x: 330, y: 20 } });
  await mpage.locator(".el-drawer:visible").waitFor();
  await shot(mpage, "手机-文件行操作 ActionSheet", { slug: "m-actionsheet", settle: 600 });
  await mpage.keyboard.press("Escape");
  await mpage.waitForTimeout(500);
});
await tryStep("手机-MCP ActionSheet", async () => {
  await mpage.goto(`${BASE}/mcp`);
  await mpage.waitForLoadState("domcontentloaded");
  await mpage.locator(".el-table__row").first().waitFor({ state: "visible", timeout: 8000 });
  await mpage.locator(".el-table__row").first().click({ position: { x: 330, y: 20 } });
  await mpage.locator(".el-drawer:visible").waitFor();
  await shot(mpage, "手机-MCP 行操作 ActionSheet", { slug: "m-mcp-actionsheet", settle: 600 });
  await mpage.keyboard.press("Escape");
  await mpage.waitForTimeout(500);
});
await mpage.close();

// ---------- plain user pass ----------
await page.goto(`${BASE}/dashboard`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(600);
await page.locator("aside button, .shell-aside button", { hasText: "登出" }).first().click();
await page.waitForURL("**/login", { timeout: 6000 });
const [ucapRes] = await Promise.all([
  page.waitForResponse((r) => r.url().includes("/api/captcha")),
  page.reload(),
]);
await page.waitForLoadState("domcontentloaded");
await page.fill("input[name=username]", "e2euser");
await page.fill("input[name=password]", "e2e-user-secret");
await page.fill(".captcha-row input", ucapRes.headers()["x-mudp-captcha-answer"]);
await page.locator("button.auth-submit").click();
await page.waitForURL("**/dashboard");
console.log("[ok] logged in as e2euser");
await page.waitForTimeout(900);
await shot(page, "普通用户-仪表盘(侧栏无 admin 项)", { slug: "u-dashboard" });
await page.goto(`${BASE}/netdisk`);
await page.waitForLoadState("domcontentloaded");
await page.locator(".el-table__row").first().waitFor({ state: "visible", timeout: 8000 });
await shot(page, "普通用户-网盘(有文件)", { slug: "u-netdisk", fullPage: true });
await page.goto(`${BASE}/settings`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(800);
await shot(page, "普通用户-设置(无 admin 区)", { slug: "u-settings", fullPage: true });
await page.goto(`${BASE}/usage`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(1200);
await shot(page, "普通用户-使用情况", { slug: "u-usage", fullPage: true });

fs.appendFileSync(path.join(OUT, "manifest.txt"), "\n" + manifest.join("\n"));
console.log(`[done] ${manifest.length} extra screenshots`);
await browser.close();
