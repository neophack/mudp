// One-off verification for the translucent fixed-column fix in
// src/styles/index.css: with the netdisk table horizontally scrolled, the
// sticky right actions column must paint an opaque background so the
// "Modified" column scrolled beneath it cannot show through, especially on
// row hover (the reported bug).
//
// Run from web/: node .dbg/netdisk-fixed-col-check.mjs
import { chromium } from "playwright";
import { spawn } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import net from "node:net";
import { request } from "@playwright/test";

const PORT = 19771;
const BASE = `http://127.0.0.1:${PORT}`;
const WEB = path.resolve(import.meta.dirname, "..");
const REPO = path.resolve(WEB, "..");
const BIN = path.join(REPO, "dist", "mudp-windows-amd64.exe");
const OUT = path.join(WEB, ".dbg", "fixed-col-out");
fs.mkdirSync(OUT, { recursive: true });

const dbPath = path.join(os.tmpdir(), `netdisk-fixcheck-${Date.now()}.db`);
const ndRoot = path.join(os.tmpdir(), `netdisk-fixcheck-nd-${Date.now()}`);

function waitForPort(port, timeout = 30000) {
  const start = Date.now();
  return new Promise((resolve, reject) => {
    (function tryOnce() {
      const s = net.Socket();
      s.setTimeout(1000);
      s.once("connect", () => { s.destroy(); resolve(); });
      s.once("error", () => { s.destroy(); Date.now() - start > timeout ? reject(new Error("port timeout")) : setTimeout(tryOnce, 200); });
      s.once("timeout", () => { s.destroy(); setTimeout(tryOnce, 200); });
      s.connect(port, "127.0.0.1");
    })();
  });
}

const results = [];
const check = (name, ok, detail = "") => {
  results.push(`${ok ? "PASS" : "FAIL"} ${name}${detail ? " — " + detail : ""}`);
};

// ---------- 1. backend ----------
fs.mkdirSync(ndRoot, { recursive: true });
const server = spawn(BIN, [], {
  cwd: REPO,
  env: {
    ...process.env,
    MUDP_ADDR: `127.0.0.1:${PORT}`,
    MUDP_DB: dbPath,
    MUDP_ADMIN_USER: "admin",
    MUDP_ADMIN_PASSWORD: "smoke-secret-123",
    MUDP_SESSION_SECRET: "e2e-session-secret-must-be-32-bytes-long",
    MUDP_CAPTCHA_TEST_ANSWERS: "1",
  },
  stdio: "pipe",
});
let serverLogs = "";
server.stdout.on("data", (d) => (serverLogs = (serverLogs + d).slice(-4000)));
server.stderr.on("data", (d) => (serverLogs = (serverLogs + d).slice(-4000)));
await waitForPort(PORT);

// ---------- 2. seed: netdisk root + long-named rows ----------
const ctx = await request.newContext({ baseURL: BASE });
const cap = await ctx.get("/api/captcha");
if (!cap.headers()["x-mudp-captcha-id"]) {
  throw new Error(`backend on :${PORT} has no captcha route — stale process hijacking the port?`);
}
const login = await ctx.post("/api/login", {
  data: {
    username: "admin",
    password: "smoke-secret-123",
    captchaId: cap.headers()["x-mudp-captcha-id"],
    captcha: cap.headers()["x-mudp-captcha-answer"],
  },
});
if (!login.ok()) throw new Error("API login failed: " + login.status());
const csrf = (await ctx.storageState()).cookies.find((c) => c.name === "mudp_csrf")?.value;
const post = (url, data) => ctx.post(url, { headers: { "X-CSRF-Token": csrf }, data });

const groups = await (await ctx.get("/api/groups")).json();
const usersGroup = (Array.isArray(groups) ? groups : groups.groups).find((g) => g.name === "users");
if (!usersGroup) throw new Error("default 'users' group missing");
const setRoot = await post("/api/groups/netdisk", { groupId: usersGroup.id, path: ndRoot });
if (!setRoot.ok()) throw new Error("set netdisk root failed: " + setRoot.status());

// The bootstrap admin joins no group (no netdisk), so browse as a plain user.
const USERNAME = "filebrowser", PASSWORD = "smoke-secret-123";
const createUser = await post("/api/users", {
  username: USERNAME,
  password: PASSWORD,
  role: "user",
  groupIds: [usersGroup.id],
  containerCap: 5,
  netdiskQuotaBytes: 2 * 1024 * 1024 * 1024,
});
if (!createUser.ok()) throw new Error("create user failed: " + createUser.status() + await createUser.text());

// Rows live under the per-user directory the server auto-creates, so make
// them through the API as the browsing user (mkdir resolves the user root).
const uctx = await request.newContext({ baseURL: BASE });
const ucap = await uctx.get("/api/captcha");
const ulogin = await uctx.post("/api/login", {
  data: {
    username: USERNAME,
    password: PASSWORD,
    captchaId: ucap.headers()["x-mudp-captcha-id"],
    captcha: ucap.headers()["x-mudp-captcha-answer"],
  },
});
if (!ulogin.ok()) throw new Error("user API login failed: " + ulogin.status());
const ucsrf = (await uctx.storageState()).cookies.find((c) => c.name === "mudp_csrf")?.value;
const LONG = "一份特别长的文件名用来把表格撑出横向滚动从而复现固定列悬浮的问题场景";
for (let i = 1; i <= 3; i++) {
  const r = await uctx.post("/api/netdisk/mkdir", { headers: { "X-CSRF-Token": ucsrf }, data: { path: `${LONG}编号${i}` } });
  if (!r.ok()) throw new Error("mkdir failed: " + r.status() + await r.text());
}
await uctx.dispose();
await ctx.dispose();

// ---------- 3. vite dev server ----------
const vite = spawn(process.execPath, [path.join(WEB, "node_modules", "vite", "bin", "vite.js"), "--port", "5193", "--strictPort"], {
  cwd: WEB,
  env: { ...process.env, MUDP_UPSTREAM: BASE },
  stdio: "pipe",
});
let viteLogs = "";
vite.stdout.on("data", (d) => (viteLogs = (viteLogs + d).slice(-4000)));
vite.stderr.on("data", (d) => (viteLogs = (viteLogs + d).slice(-4000)));
process.on("unhandledRejection", (err) => {
  console.error(err);
  console.log("--- vite logs ---\n" + viteLogs);
  console.log("--- server logs ---\n" + serverLogs);
  try { vite.kill(); } catch {}
  try { server.kill(); } catch {}
  process.exit(1);
});
await waitForPort(5193);
await new Promise((r) => setTimeout(r, 1500));

// ---------- 4. browser ----------
const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 980, height: 800 } });

// UI login (captcha answer comes from the test-mode response header).
await page.goto("http://127.0.0.1:5193/");
await page.fill("input[name='username']", USERNAME);
await page.fill("input[name='password']", PASSWORD);
const [capResp] = await Promise.all([
  page.waitForResponse((r) => r.url().includes("/api/captcha"), { timeout: 15000 }),
  page.click(".captcha-img"),
]);
console.log("CAPTCHA RESP:", capResp.url(), capResp.status(), JSON.stringify(capResp.headers()));
await page.fill("input[name='captcha']", capResp.headers()["x-mudp-captcha-answer"]);
await page.click("form.auth-card .auth-submit");
await page.waitForSelector(".work-header", { timeout: 30000 });

await page.goto("http://127.0.0.1:5193/netdisk");
page.on("console", (m) => { if (m.type() === "error") console.log("[console.error]", m.text()); });
page.on("response", (r) => { if (r.url().includes("/api/netdisk")) console.log("[netdisk api]", r.status(), r.url()); });
try {
  await page.waitForSelector(".el-table__body-wrapper tbody tr", { timeout: 15000 });
} catch {
  await page.screenshot({ path: path.join(OUT, "debug-no-rows.png") });
  throw new Error("netdisk rows never rendered — see debug-no-rows.png");
}

// Make sure the table really overflows horizontally, then scroll fully right.
const overflow = await page.evaluate(() => {
  const scroller = document.querySelector(".el-table .el-scrollbar__wrap") || document.querySelector(".el-table__body-wrapper");
  return { scrollWidth: scroller.scrollWidth, clientWidth: scroller.clientWidth };
});
check("table overflows horizontally", overflow.scrollWidth > overflow.clientWidth, `${overflow.scrollWidth} vs ${overflow.clientWidth}`);
await page.evaluate(() => {
  const scroller = document.querySelector(".el-table .el-scrollbar__wrap") || document.querySelector(".el-table__body-wrapper");
  scroller.scrollLeft = scroller.scrollWidth;
});

// Hover a row so the (formerly translucent) hover fill kicks in, then inspect
// the sticky action cell's computed background.
const row = page.locator(".el-table__body-wrapper tbody tr").first();
await row.locator(".row-actions").hover();
await page.waitForTimeout(300);

const style = await page.evaluate(() => {
  const td = document.querySelector(".el-table__body-wrapper tbody tr td.el-table-fixed-column--right");
  const th = document.querySelector(".el-table__header-wrapper th.el-table-fixed-column--right");
  const cs = getComputedStyle(td);
  const csH = getComputedStyle(th);
  const alphaOf = (c) => (c === "transparent" ? 0 : c.includes("rgba") ? Number(c.split(",")[3].replace(")", "")) : 1);
  return {
    tdColor: cs.backgroundColor,
    tdAlpha: alphaOf(cs.backgroundColor),
    tdImage: cs.backgroundImage,
    thColor: cs.backgroundColor,
    thAlpha: alphaOf(cs.backgroundColor),
    thImage: cs.backgroundImage,
  };
});
check("hovered sticky body cell is opaque", style.tdAlpha === 1, `${style.tdColor} / ${style.tdImage}`);
check("sticky header cell is opaque", style.thAlpha === 1, `${style.thColor} / ${style.thImage}`);
await page.screenshot({ path: path.join(OUT, "after-fix-light.png") });

// Dark mode: same assertions against the dark token set.
await page.evaluate(() => document.documentElement.setAttribute("data-theme", "dark"));
await page.evaluate(() => document.documentElement.classList.add("dark"));
await page.waitForTimeout(300);
const darkStyle = await page.evaluate(() => {
  const td = document.querySelector(".el-table__body-wrapper tbody tr td.el-table-fixed-column--right");
  const cs = getComputedStyle(td);
  const alphaOf = (c) => (c === "transparent" ? 0 : c.includes("rgba") ? Number(c.split(",")[3].replace(")", "")) : 1);
  return { color: cs.backgroundColor, alpha: alphaOf(cs.backgroundColor) };
});
check("dark-mode sticky body cell is opaque", darkStyle.alpha === 1, darkStyle.color);
await page.screenshot({ path: path.join(OUT, "after-fix-dark.png") });
await page.evaluate(() => {
  document.documentElement.removeAttribute("data-theme");
  document.documentElement.classList.remove("dark");
});

// "Before" reproduction for visual comparison: neutralize the fix in-page.
await page.addStyleTag({
  content: `
    .el-table th.el-table-fixed-column--right { background: var(--table-head) !important; }
    .el-table__body td.el-table-fixed-column--right { background-color: rgba(0,0,0,0.05) !important; background-image: none !important; }
  `,
});
await page.waitForTimeout(300);
await page.screenshot({ path: path.join(OUT, "before-fix-light.png") });

await browser.close();

// ---------- teardown ----------
try { vite.kill(); } catch {}
try { server.kill(); } catch {}
setTimeout(() => {
  for (const p of [dbPath, dbPath + "-shm", dbPath + "-wal"]) fs.rmSync(p, { force: true });
  fs.rmSync(ndRoot, { recursive: true, force: true });
}, 1000);

console.log(results.join("\n"));
const failed = results.some((r) => r.startsWith("FAIL"));
if (failed) {
  console.log("\n--- server logs ---\n" + serverLogs.slice(-2000));
  console.log("\n--- vite logs ---\n" + viteLogs.slice(-2000));
}
process.exit(failed ? 1 : 0);
