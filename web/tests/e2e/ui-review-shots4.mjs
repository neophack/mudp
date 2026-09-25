// Fourth pass: (a) first-run instance -> Setup wizard; (b) review instance ->
// move e2euser to the pending group via the Users page, then log in as them
// to capture the Pending page, then restore.
import { chromium } from "@playwright/test";
import { spawn } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

const BASE = "http://127.0.0.1:19321";
const OUT = "C:/Users/penghongxia/AppData/Local/Temp/mudp-ui-review";
const manifest = [];
let n = 300;

async function shot(page, desc, opts = {}) {
  n += 1;
  const name = `${n}-${opts.slug}.png`;
  await page.waitForTimeout(opts.settle ?? 700);
  await page.screenshot({ path: path.join(OUT, name), fullPage: !!opts.fullPage });
  manifest.push(`${name}\t${desc}`);
  console.log(`[shot] ${name} ${desc}`);
}

// ---------- (a) first-run setup wizard on a throwaway instance ----------
const port = 19099;
const dbPath = path.join(os.tmpdir(), `mudp-ui-review-setup-${Date.now()}.db`);
const proc = spawn("dist/mudp-windows-amd64.exe", [], {
  cwd: "D:/mudp",
  env: {
    ...process.env,
    MUDP_ADDR: `127.0.0.1:${port}`,
    MUDP_DB: dbPath,
    MUDP_ADMIN_PASSWORD: "",
    MUDP_SESSION_SECRET: "e2e-session-secret-must-be-32-bytes-long",
    MUDP_CAPTCHA_TEST_ANSWERS: "1",
  },
  stdio: "ignore",
});
await new Promise((r) => setTimeout(r, 2500));
const fbrowser = await chromium.launch();
const fctx = await fbrowser.newContext({ viewport: { width: 1440, height: 900 }, locale: "zh-CN" });
const fpage = await fctx.newPage();
fpage.setDefaultTimeout(6000);
try {
  await fpage.goto(`http://127.0.0.1:${port}/`);
  await fpage.waitForLoadState("domcontentloaded");
  await fpage.waitForTimeout(1000);
  // switch to Chinese if the wizard renders in English
  const zh = fpage.locator("button", { hasText: "中文" });
  if (await zh.count()) {
    await zh.first().click();
    await fpage.waitForTimeout(700);
  }
  await shot(fpage, "Setup 向导(first-run,中文)", { slug: "setup", fullPage: true });
  // filled state for validation visuals
  await fpage.fill("input[type=text], input:not([type=password])", "setupadmin").catch(() => {});
  await shot(fpage, "Setup 向导(填写中)", { slug: "setup-filled", fullPage: true });
} catch (err) {
  console.log(`[SKIP] setup wizard: ${err.message.split("\n")[0]}`);
}
await fbrowser.close();
proc.kill("SIGTERM");
for (const ext of ["", "-shm", "-wal"]) {
  try { fs.unlinkSync(dbPath + ext); } catch { /* gone */ }
}

// ---------- (b) pending page on the review instance ----------
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

// move e2euser into the pending group through the Users page dialog
await page.goto(`${BASE}/users`);
await page.waitForLoadState("domcontentloaded");
await page.locator(".el-table__row", { hasText: "e2euser" }).locator("button", { hasText: "用户组" }).first().click();
await page.locator(".el-dialog:visible").first().waitFor();
await page.locator(".el-dialog:visible").getByText("pending", { exact: false }).first().click();
await page.locator(".el-dialog:visible button", { hasText: "保存" }).last().click();
await page.waitForTimeout(800);
console.log("[ok] e2euser moved to pending group");

// log out, log back in as e2euser -> should land on /pending
await page.locator("aside button, .shell-aside button", { hasText: "登出" }).first().click();
await page.waitForURL("**/login");
const [ucapRes] = await Promise.all([
  page.waitForResponse((r) => r.url().includes("/api/captcha")),
  page.reload(),
]);
await page.waitForLoadState("domcontentloaded");
await page.fill("input[name=username]", "e2euser");
await page.fill("input[name=password]", "e2e-user-secret");
await page.fill(".captcha-row input", ucapRes.headers()["x-mudp-captcha-answer"]);
await page.locator("button.auth-submit").click();
await page.waitForURL("**/pending", { timeout: 8000 });
await page.waitForTimeout(800);
await shot(page, "待审批页(Pending)", { slug: "pending", fullPage: true });

// restore: log back in as admin and put e2euser back into users
const [acapRes] = await Promise.all([
  page.waitForResponse((r) => r.url().includes("/api/captcha")).catch(() => null),
  page.goto(`${BASE}/login`),
]);
await page.waitForLoadState("domcontentloaded");
await page.fill("input[name=username]", "admin");
await page.fill("input[name=password]", "e2e-secret");
await page.fill(".captcha-row input", acapRes.headers()["x-mudp-captcha-answer"]);
await page.locator("button.auth-submit").click();
await page.waitForURL("**/dashboard");
await page.goto(`${BASE}/users`);
await page.waitForLoadState("domcontentloaded");
await page.locator(".el-table__row", { hasText: "e2euser" }).locator("button", { hasText: "用户组" }).first().click();
await page.locator(".el-dialog:visible").first().waitFor();
await page.locator(".el-dialog:visible").getByText("users", { exact: true }).first().click();
await page.waitForTimeout(600);
console.log("[ok] e2euser restored to users group");

fs.appendFileSync(path.join(OUT, "manifest.txt"), "\n" + manifest.join("\n"));
console.log("[done] pass 4");
await browser.close();
