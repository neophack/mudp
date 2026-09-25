// Manual UI review: boots a headless Chromium against the already-running
// review server (port 19321), walks every page + dialog, and saves PNGs to
// OUT for visual analysis. Purely read-only except harmless seed mutations
// (one folder, one share link, one MCP token, one user group).
import { chromium } from "@playwright/test";
import fs from "node:fs";
import path from "node:path";

const BASE = "http://127.0.0.1:19321";
const OUT = "C:/Users/penghongxia/AppData/Local/Temp/mudp-ui-review";
fs.mkdirSync(OUT, { recursive: true });

const manifest = [];
let n = 0;

async function shot(page, desc, opts = {}) {
  n += 1;
  const name = `${String(n).padStart(2, "0")}-${opts.slug || "shot"}.png`;
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
  for (let i = 0; i < 3; i += 1) {
    const boxes = await page.locator(".el-overlay:not([style*='display: none'])").count();
    if (!boxes) break;
    await page.keyboard.press("Escape");
    await page.waitForTimeout(450);
  }
}

async function clickButton(page, text, scope = "page") {
  const loc = page.locator("button", { hasText: text }).filter({ hasNotText: "忽略" });
  const count = await loc.count();
  if (count < 1) throw new Error(`button "${text}" not found`);
  await loc.first().click();
}

const browser = await chromium.launch();
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: "zh-CN" });
const page = await ctx.newPage();
page.setDefaultTimeout(5000);

// ---------- login page ----------
// capture the page's own captcha response so the answer matches what is shown
const [capRes] = await Promise.all([
  page.waitForResponse((r) => r.url().includes("/api/captcha")),
  page.goto(`${BASE}/login`),
]);
const loginCaptcha = capRes.headers()["x-mudp-captcha-answer"];
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(800);
await shot(page, "登录页(默认语言-英文)", { slug: "login-en", fullPage: true });
await tryStep("切换中文", async () => {
  await page.locator("button", { hasText: "中文" }).first().click();
  await page.waitForTimeout(900);
  await shot(page, "登录页(中文)", { slug: "login", fullPage: true });
});

// wrong-password toast state (captcha answer matches the freshly displayed one)
const [capRes2] = await Promise.all([
  page.waitForResponse((r) => r.url().includes("/api/captcha")),
  page.reload(),
]);
await page.waitForLoadState("domcontentloaded");
await tryStep("登录失败提示", async () => {
  await page.fill("input[name=username]", "admin");
  await page.fill("input[name=password]", "wrong-pass");
  await page.fill('.captcha-row input', capRes2.headers()["x-mudp-captcha-answer"]);
  await page.locator("button.auth-submit").click();
  await shot(page, "登录失败错误提示", { slug: "login-error", settle: 900 });
});

// real login
const [capRes3] = await Promise.all([
  page.waitForResponse((r) => r.url().includes("/api/captcha")),
  page.reload(),
]);
await page.waitForLoadState("domcontentloaded");
await page.fill("input[name=username]", "admin");
await page.fill("input[name=password]", "e2e-secret");
await page.fill('.captcha-row input', capRes3.headers()["x-mudp-captcha-answer"]);
await page.locator("button.auth-submit").click();
await page.waitForURL("**/dashboard", { timeout: 8000 });
console.log("[ok] logged in as admin");

// ---------- dashboard ----------
await page.goto(`${BASE}/dashboard`);
await page.waitForLoadState("domcontentloaded");
await shot(page, "仪表盘", { slug: "dashboard", fullPage: true });
await tryStep("系统更新弹窗", async () => {
  await clickButton(page, "一键升级");
  await page.locator(".el-dialog").waitFor({ state: "visible" });
  await shot(page, "系统更新弹窗(UpgradeDialog)", { slug: "upgrade-dialog" });
  await closeOverlays(page);
});

// ---------- netdisk ----------
await page.goto(`${BASE}/netdisk`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(2500);
await shot(page, "网盘(列表+外链卡)", { slug: "netdisk", fullPage: true });

await tryStep("新建文件夹 prompt", async () => {
  await clickButton(page, "新建文件夹");
  await page.locator(".el-message-box").waitFor({ state: "visible" });
  await shot(page, "新建文件夹输入框(ElMessageBox.prompt)", { slug: "netdisk-mkdir" });
  await page.locator(".el-message-box__input input").fill("走查文件夹");
  await clickButton(page, "确定");
  await page.waitForTimeout(800);
});

await tryStep("勾选文件出现批量条", async () => {
  await page.locator(".el-table__row .el-checkbox").first().click();
  await shot(page, "网盘批量操作条", { slug: "netdisk-batch", settle: 500 });
});

await tryStep("分享弹窗-表单步", async () => {
  await clickButton(page, /分享 \(/);
  await page.locator(".el-dialog").waitFor({ state: "visible" });
  await shot(page, "分享弹窗-表单步(ShareDialog)", { slug: "share-form" });
  await clickButton(page, "创建链接");
  await page.waitForTimeout(1200);
  await shot(page, "分享弹窗-结果步(分享链接已生成)", { slug: "share-result" });
  await closeOverlays(page);
});

await tryStep("复制目标选择器 FolderPicker", async () => {
  await clickButton(page, /复制 \(/);
  await page.locator(".el-dialog").waitFor({ state: "visible" });
  await shot(page, "复制/移动目标选择弹窗(FolderPicker)", { slug: "folder-picker" });
  await closeOverlays(page);
});

await tryStep("文本预览 Viewer", async () => {
  await page.locator(".name-link", { hasText: "说明文档.txt" }).first().click();
  await page.waitForTimeout(1200);
  await shot(page, "文件预览弹窗-文本(ViewerDialog)", { slug: "viewer-text" });
  await closeOverlays(page);
});
await tryStep("图片预览 Viewer", async () => {
  await page.locator(".name-link", { hasText: "logo.png" }).first().click();
  await page.waitForTimeout(1500);
  await shot(page, "文件预览弹窗-图片(ViewerDialog)", { slug: "viewer-image" });
  await closeOverlays(page);
});
await closeOverlays(page);

// ---------- containers ----------
await page.goto(`${BASE}/containers`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(1000);
await shot(page, "容器页(空状态)", { slug: "containers", fullPage: true });
await tryStep("新建容器弹窗", async () => {
  await clickButton(page, "新建容器");
  await page.waitForTimeout(900);
  await shot(page, "新建容器弹窗-表单(CreateDialog)", { slug: "container-create" });
  await closeOverlays(page);
});

// ---------- mcp ----------
await page.goto(`${BASE}/mcp`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(1000);
await shot(page, "MCP 页", { slug: "mcp", fullPage: true });
await tryStep("创建 MCP 令牌并打开配置弹窗", async () => {
  await clickButton(page, "创建令牌");
  await page.waitForTimeout(1500);
  await shot(page, "MCP 配置弹窗(创建后自动打开)", { slug: "mcp-config" });
  await closeOverlays(page);
});
await tryStep("MCP 使用记录弹窗", async () => {
  await page.locator("button", { hasText: "使用记录" }).first().click();
  await page.locator(".el-dialog").waitFor({ state: "visible" });
  await shot(page, "MCP 使用记录弹窗", { slug: "mcp-usage" });
  await closeOverlays(page);
});

// ---------- processes / usage ----------
await page.goto(`${BASE}/processes`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(1500);
await shot(page, "进程页", { slug: "processes", fullPage: true });

await page.goto(`${BASE}/usage`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(1500);
await shot(page, "用量页", { slug: "usage", fullPage: true });

// ---------- images ----------
await page.goto(`${BASE}/images`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(1000);
await shot(page, "镜像页(空状态)", { slug: "images", fullPage: true });
await tryStep("构建镜像弹窗", async () => {
  await clickButton(page, "构建");
  await page.locator(".el-dialog").waitFor({ state: "visible" });
  await shot(page, "构建镜像弹窗", { slug: "image-build" });
  await closeOverlays(page);
});
await tryStep("导入镜像弹窗", async () => {
  await clickButton(page, "导入");
  await page.locator(".el-dialog").waitFor({ state: "visible" });
  await shot(page, "导入镜像弹窗", { slug: "image-import" });
  await closeOverlays(page);
});
await tryStep("注册镜像弹窗", async () => {
  await clickButton(page, "注册");
  await page.locator(".el-dialog").waitFor({ state: "visible" });
  await shot(page, "注册镜像弹窗", { slug: "image-register" });
  await closeOverlays(page);
});
await tryStep("拉取镜像弹窗", async () => {
  await clickButton(page, "拉取镜像");
  await page.locator(".el-dialog").waitFor({ state: "visible" });
  await shot(page, "拉取镜像弹窗", { slug: "image-pull" });
  await closeOverlays(page);
});

// ---------- volumes ----------
await page.goto(`${BASE}/volumes`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(1000);
await shot(page, "卷页(空状态)", { slug: "volumes", fullPage: true });
await tryStep("新建卷弹窗", async () => {
  await clickButton(page, "新建卷");
  await page.locator(".el-dialog").waitFor({ state: "visible" });
  await shot(page, "新建卷弹窗", { slug: "volume-new" });
  await closeOverlays(page);
});
await tryStep("清理未使用确认框", async () => {
  await clickButton(page, "清理未使用");
  await page.locator(".el-message-box").waitFor({ state: "visible" });
  await shot(page, "清理未使用卷确认框", { slug: "volume-prune" });
  await page.locator(".el-message-box__btns button").first().click();
  await page.waitForTimeout(400);
});

// ---------- networks ----------
await page.goto(`${BASE}/networks`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(1000);
await shot(page, "网络页", { slug: "networks", fullPage: true });
await tryStep("新建网络弹窗", async () => {
  await clickButton(page, "新建网络");
  await page.locator(".el-dialog").waitFor({ state: "visible" });
  await shot(page, "新建网络弹窗", { slug: "network-new" });
  await closeOverlays(page);
});

// ---------- forwards ----------
await page.goto(`${BASE}/forwards`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(1000);
await shot(page, "端口转发页(admin)", { slug: "forwards", fullPage: true });
await tryStep("添加转发弹窗", async () => {
  await clickButton(page, "添加转发");
  await page.locator(".el-dialog").waitFor({ state: "visible" });
  await shot(page, "添加转发弹窗", { slug: "forward-add" });
  await closeOverlays(page);
});

// ---------- stacks ----------
await page.goto(`${BASE}/stacks`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(1000);
await shot(page, "堆栈页(空状态)", { slug: "stacks", fullPage: true });
await tryStep("新建堆栈编辑器弹窗", async () => {
  await clickButton(page, "新建堆栈");
  await page.locator(".el-dialog").waitFor({ state: "visible" });
  await page.waitForTimeout(600);
  await shot(page, "新建堆栈编辑器弹窗", { slug: "stack-new" });
  await closeOverlays(page);
});

// ---------- hardware ----------
await page.goto(`${BASE}/hardware`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(2000);
await shot(page, "硬件监控页", { slug: "hardware", fullPage: true });

// ---------- users ----------
await page.goto(`${BASE}/users`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(1200);
await shot(page, "用户页(admin,五区块)", { slug: "users", fullPage: true });
await tryStep("新建用户弹窗", async () => {
  await clickButton(page, "新建用户");
  await page.locator(".el-dialog").waitFor({ state: "visible" });
  await shot(page, "新建用户弹窗", { slug: "user-new" });
  await closeOverlays(page);
});
await tryStep("用户编辑弹窗", async () => {
  await page.locator("button", { hasText: "编辑" }).first().click();
  await page.locator(".el-dialog").waitFor({ state: "visible" });
  await shot(page, "用户编辑弹窗", { slug: "user-edit" });
  await closeOverlays(page);
});
await tryStep("用户组设置弹窗", async () => {
  await page.locator(".el-table__row button", { hasText: "编辑" }).last().click();
  await page.locator(".el-dialog").waitFor({ state: "visible" });
  await shot(page, "用户组设置弹窗", { slug: "group-settings" });
  await closeOverlays(page);
});
await tryStep("新建用户组 prompt", async () => {
  await clickButton(page, "新建用户组");
  await page.locator(".el-message-box").waitFor({ state: "visible" });
  await shot(page, "新建用户组输入框", { slug: "group-new" });
  await page.locator(".el-message-box__input input").fill("走查组");
  await clickButton(page, "确定");
  await page.waitForTimeout(800);
});

// ---------- audit / security ----------
await page.goto(`${BASE}/audit`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(1200);
await shot(page, "审计页(admin)", { slug: "audit", fullPage: true });

await page.goto(`${BASE}/security`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(1800);
await shot(page, "安全监控-概览(admin)", { slug: "security-overview", fullPage: true });
await tryStep("安全监控-访问记录", async () => {
  await page.locator(".el-radio-button", { hasText: "访问记录" }).first().click();
  await shot(page, "安全监控-访问记录", { slug: "security-logs", settle: 1200 });
});
await tryStep("安全监控-设置", async () => {
  await page.locator(".el-radio-button", { hasText: "设置" }).first().click();
  await shot(page, "安全监控-设置", { slug: "security-settings", settle: 800, fullPage: true });
});
await tryStep("安全监控-MCP", async () => {
  await page.locator(".el-radio-button").last().click();
  await shot(page, "安全监控-MCP", { slug: "security-mcp", settle: 1200 });
});

// ---------- errors / disks / database ----------
await page.goto(`${BASE}/errors`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(1000);
await shot(page, "错误监控页(admin)", { slug: "errors", fullPage: true });

await page.goto(`${BASE}/disks`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(1500);
await shot(page, "磁盘管理页(admin)", { slug: "disks", fullPage: true });

await page.goto(`${BASE}/database`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(1000);
await shot(page, "数据库页(admin)", { slug: "database", fullPage: true });
await tryStep("数据库清理弹窗", async () => {
  await page.locator("button", { hasText: "清理" }).first().click();
  await page.locator(".el-dialog").waitFor({ state: "visible" });
  await shot(page, "数据库清理弹窗", { slug: "database-clean" });
  await closeOverlays(page);
});

// ---------- settings ----------
await page.goto(`${BASE}/settings`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(1000);
await shot(page, "设置页(admin 区可见)", { slug: "settings", fullPage: true });
await tryStep("添加仓库弹窗", async () => {
  await clickButton(page, "添加仓库");
  await page.locator(".el-dialog").waitFor({ state: "visible" });
  await shot(page, "添加仓库弹窗", { slug: "registry-new" });
  await closeOverlays(page);
});

// ---------- help ----------
await page.goto(`${BASE}/help`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(800);
await shot(page, "帮助页", { slug: "help", fullPage: true });

// ---------- global overlays ----------
await page.goto(`${BASE}/dashboard`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(800);
await tryStep("后台任务面板", async () => {
  await page.locator(".head-actions .el-badge").first().click();
  await page.locator(".el-dialog").waitFor({ state: "visible" });
  await shot(page, "后台任务面板(JobsPanel)", { slug: "jobs-panel" });
  await closeOverlays(page);
});
await tryStep("通知面板", async () => {
  await page.locator(".head-actions .el-badge").nth(1).click();
  await page.locator(".el-dialog").waitFor({ state: "visible" });
  await shot(page, "通知面板(NotificationsPanel)", { slug: "notif-panel" });
  await closeOverlays(page);
});
await tryStep("暗色主题", async () => {
  await page.locator('button[title*="外观"]').first().click();
  await shot(page, "仪表盘-暗色主题", { slug: "dashboard-dark", settle: 600 });
  await page.locator('button[title*="外观"]').first().click();
  await page.waitForTimeout(400);
});
await tryStep("侧栏折叠态", async () => {
  await page.locator('button[aria-label="Toggle sidebar"]').click();
  await shot(page, "侧栏折叠态", { slug: "sidebar-collapsed", settle: 500 });
  await page.locator('button[aria-label="Toggle sidebar"]').click();
  await page.waitForTimeout(400);
});

// ---------- mobile viewport ----------
const mpage = await ctx.newPage();
await mpage.setViewportSize({ width: 390, height: 844 });
mpage.setDefaultTimeout(5000);
await mpage.goto(`${BASE}/dashboard`);
await mpage.waitForLoadState("domcontentloaded");
await mpage.waitForTimeout(1000);
await shot(mpage, "手机-仪表盘", { slug: "m-dashboard" });
await tryStep("手机-抽屉导航", async () => {
  await mpage.locator('button[aria-label*="菜单"]').click();
  await mpage.locator(".el-drawer").waitFor({ state: "visible" });
  await shot(mpage, "手机-抽屉导航", { slug: "m-drawer" });
  await mpage.keyboard.press("Escape");
  await mpage.waitForTimeout(500);
});
await mpage.goto(`${BASE}/netdisk`);
await mpage.waitForLoadState("domcontentloaded");
await mpage.waitForTimeout(1500);
await mpage.waitForTimeout(600);
await shot(mpage, "手机-网盘", { slug: "m-netdisk" });
await tryStep("手机-行操作 ActionSheet", async () => {
  await mpage.locator(".el-table__row").first().click({ position: { x: 300, y: 20 } });
  await mpage.locator(".el-drawer").waitFor({ state: "visible" });
  await shot(mpage, "手机-文件行操作 ActionSheet", { slug: "m-actionsheet" });
  await mpage.keyboard.press("Escape");
  await mpage.waitForTimeout(500);
});
await mpage.goto(`${BASE}/containers`);
await mpage.waitForLoadState("domcontentloaded");
await mpage.waitForTimeout(1000);
await shot(mpage, "手机-容器页", { slug: "m-containers" });
await mpage.close();

// ---------- plain user pass ----------
await tryStep("退出 admin", async () => {
  await page.goto(`${BASE}/dashboard`);
  await page.waitForLoadState("domcontentloaded");
  await clickButton(page, "退出登录");
  await page.waitForURL("**/login", { timeout: 6000 });
});
const [ucapRes] = await Promise.all([
  page.waitForResponse((r) => r.url().includes("/api/captcha")),
  page.reload(),
]);
await page.waitForLoadState("domcontentloaded");
await page.fill("input[name=username]", "e2euser");
await page.fill("input[name=password]", "e2e-user-secret");
await page.fill('.captcha-row input', ucapRes.headers()["x-mudp-captcha-answer"]);
await page.locator("button.auth-submit").click();
await page.waitForURL("**/dashboard", { timeout: 8000 });
console.log("[ok] logged in as e2euser");
await page.waitForTimeout(800);
await shot(page, "普通用户-仪表盘(侧栏无 admin 项)", { slug: "u-dashboard" });
await page.goto(`${BASE}/netdisk`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(1500);
await shot(page, "普通用户-网盘", { slug: "u-netdisk", fullPage: true });
await page.goto(`${BASE}/settings`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(800);
await shot(page, "普通用户-设置(无 admin 区)", { slug: "u-settings", fullPage: true });

fs.writeFileSync(path.join(OUT, "manifest.txt"), manifest.join("\n"));
console.log(`[done] ${n} screenshots -> ${OUT}`);
await browser.close();
