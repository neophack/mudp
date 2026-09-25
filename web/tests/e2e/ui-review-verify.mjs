// Regression pass after the UI polish batch: re-shoot each fixed spot.
import { chromium } from "@playwright/test";
import fs from "node:fs";
import path from "node:path";

const BASE = "http://127.0.0.1:19321";
const OUT = "C:/Users/penghongxia/AppData/Local/Temp/mudp-ui-review";
const manifest = [];
let n = 500;

async function shot(page, desc, opts = {}) {
  n += 1;
  const name = `${n}-${opts.slug}.png`;
  await page.waitForTimeout(opts.settle ?? 700);
  await page.screenshot({ path: path.join(OUT, name), fullPage: !!opts.fullPage });
  manifest.push(`${name}\t${desc}`);
  console.log(`[shot] ${name} ${desc}`);
}

async function login(page, username, password, ctx) {
  const [capRes] = await Promise.all([
    page.waitForResponse((r) => r.url().includes("/api/captcha")),
    page.goto(`${BASE}/login`),
  ]);
  await page.waitForLoadState("domcontentloaded");
  await page.fill("input[name=username]", username);
  await page.fill("input[name=password]", password);
  await page.fill(".captcha-row input", capRes.headers()["x-mudp-captcha-answer"]);
  await page.locator("button.auth-submit").click();
  await page.waitForURL((u) => !u.pathname.endsWith("/login"), { timeout: 10000 });
  await page.waitForTimeout(600);
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

// 1. login page should now come up in Chinese via browser-locale detection
await page.goto(`${BASE}/login`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(900);
await shot(page, "登录页-浏览器语言探测默认中文", { slug: "v-login-zh", fullPage: true });

// 2. localized bad-credentials toast
await page.fill("input[name=username]", "admin");
await page.fill("input[name=password]", "totally-wrong");
await page.fill(".captcha-row input", "0000");
await page.locator("button.auth-submit").click();
await shot(page, "登录失败-本地化错误提示", { slug: "v-login-error", settle: 900 });

await login(page, "admin", "e2e-secret", ctx);
await shot(page, "仪表盘-admin(环境卡+空容器灰环)", { slug: "v-dashboard-admin", fullPage: true });

// 3. container create dialog: hint row + unbroken checkboxes
await page.goto(`${BASE}/containers`);
await page.waitForLoadState("domcontentloaded");
await page.locator("button", { hasText: "新建容器" }).first().click();
await page.locator(".el-dialog:visible").first().waitFor();
await page.waitForTimeout(500);
await shot(page, "新建容器-空镜像引导+checkbox完整", { slug: "v-container-create", fullPage: true });
await closeOverlays(page);

// 4. netdisk toolbar: batch buttons hidden until a row is checked
await page.goto(`${BASE}/netdisk`);
await page.waitForLoadState("domcontentloaded");
await page.locator(".el-table__row").first().waitFor({ state: "visible", timeout: 8000 });
await shot(page, "网盘-未选中时无批量按钮", { slug: "v-netdisk-nobatch" });
await page.locator(".el-table__row .el-checkbox").first().click();
await page.waitForTimeout(400);
await shot(page, "网盘-勾选后批量按钮出现", { slug: "v-netdisk-batch" });

// 5. uploads overlay in light theme
const fileInputs = page.locator('input[type="file"]');
if (await fileInputs.count()) {
  const tmp = path.join(OUT, "v-upload.png");
  fs.copyFileSync("D:/mudp/web/dist/mudp.png", tmp);
  await fileInputs.first().setInputFiles([tmp]);
  await page.waitForTimeout(600);
  await shot(page, "上传浮层-跟随浅色主题", { slug: "v-upload-light", settle: 300 });
  await page.waitForTimeout(2000);
  await closeOverlays(page);
}

// 6. images / volumes / stacks search inputs
await page.goto(`${BASE}/images`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(800);
await shot(page, "镜像页-搜索框", { slug: "v-images-search" });
await page.goto(`${BASE}/volumes`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(800);
await shot(page, "卷页-搜索框", { slug: "v-volumes-search" });
await page.goto(`${BASE}/stacks`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(800);
await shot(page, "堆栈页-搜索框", { slug: "v-stacks-search" });

// 7. new-user dialog defaults to the users group
await page.goto(`${BASE}/users`);
await page.waitForLoadState("domcontentloaded");
await page.locator("button", { hasText: "新建用户" }).first().click();
await page.locator(".el-dialog:visible").first().waitFor();
await shot(page, "新建用户-默认选中users组", { slug: "v-user-new", settle: 500 });
await closeOverlays(page);

// 8. errors page: renamed action + clear-all confirmation
await page.goto(`${BASE}/errors`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(900);
await page.locator("button", { hasText: "清空" }).first().click();
await page.locator(".el-message-box:visible").waitFor();
await shot(page, "错误监控-清空确认框+标记解决文案", { slug: "v-errors-clear", settle: 400 });
await page.locator(".el-message-box__btns button").first().click();
await page.waitForTimeout(400);

// 9. MCP: no-container confirm with jump
await page.goto(`${BASE}/mcp`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(800);
await page.locator("button", { hasText: "创建令牌" }).first().click();
await page.locator(".el-message-box:visible").waitFor();
await shot(page, "MCP-空容器引导确认框", { slug: "v-mcp-nocontainer", settle: 400 });
await page.locator(".el-message-box__btns button").first().click();
await page.waitForTimeout(400);

// 10. disks: padded schedule time
await page.goto(`${BASE}/disks`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(1200);
await shot(page, "磁盘-备份时间补零显示", { slug: "v-disks-time" });

// 11. plain user dashboard: no environment card
await page.goto(`${BASE}/dashboard`);
await page.waitForLoadState("domcontentloaded");
await page.waitForTimeout(600);
await page.locator("aside button", { hasText: "登出" }).first().click();
await page.waitForURL("**/login");
await login(page, "e2euser", "e2e-user-secret");
await shot(page, "仪表盘-普通用户(无环境卡)", { slug: "v-dashboard-user", fullPage: true });

fs.appendFileSync(path.join(OUT, "manifest.txt"), "\n" + manifest.join("\n"));
console.log("[done] verification pass");
await browser.close();
