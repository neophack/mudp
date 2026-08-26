// One-off verification of the redesign: dashboard tiles/version banner/env
// grid, iOS settings rows, and the mobile drawer user header + logout.
// Run: node .dbg/verify-redesign.mjs  (server on 127.0.0.1:19100)
import { chromium } from "playwright";

const BASE = "http://127.0.0.1:19100";
const browser = await chromium.launch();

async function login(page) {
  await page.goto(BASE + "/login", { waitUntil: "domcontentloaded" });
  await page.waitForSelector("form.auth-card");
  await page.fill("input[name='username']", "admin");
  await page.fill("input[name='password']", "e2e-secret");
  await page.click("form.auth-card .auth-submit");
  await page.waitForSelector(".work-header", { timeout: 30000 });
}

// ---- Dashboard (desktop) ----
const desk = await browser.newPage({ viewport: { width: 1280, height: 800 } });
await login(desk);
await desk.waitForTimeout(2500);
const dash = await desk.evaluate(() => {
  const tiles = [...document.querySelectorAll(".stat-tile")].map((t) => ({
    icon: !!t.querySelector(".stat-icon svg"),
    bg: t.querySelector(".stat-icon") ? getComputedStyle(t.querySelector(".stat-icon")).backgroundColor : null,
    value: t.querySelector(".stat-value")?.textContent,
  }));
  return {
    tiles,
    emojiLeft: [...document.querySelectorAll(".dash-stack")].map((n) => n.textContent).join("").match(/[\u{1F300}-\u{1FAFF}]/gu)?.length || 0,
    verBanner: !!document.querySelector(".ver-banner"),
    envItems: document.querySelectorAll(".env-grid .env-item").length,
    envCols: getComputedStyle(document.querySelector(".env-grid")).gridTemplateColumns.split(" ").length,
    hScroll: document.documentElement.scrollWidth > document.documentElement.clientWidth,
  };
});
console.log("DASH", JSON.stringify(dash, null, 2));
await desk.screenshot({ path: ".dbg/redash-desktop.png" });

// ---- Settings (desktop) ----
await desk.goto(BASE + "/settings", { waitUntil: "domcontentloaded" });
await desk.waitForSelector(".settings-page");
await desk.waitForTimeout(800);
const set = await desk.evaluate(() => ({
  rows: document.querySelectorAll(".settings-page .row").length,
  icons: document.querySelectorAll(".settings-page .row-icon svg").length,
  selects: document.querySelectorAll(".settings-page .row-select").length,
  switches: document.querySelectorAll(".settings-page .el-switch").length,
  pageW: Math.round(document.querySelector(".settings-page").getBoundingClientRect().width),
  saveButtons: [...document.querySelectorAll(".settings-page button")].filter((b) => /保存|Save/.test(b.textContent)).length,
  hScroll: document.documentElement.scrollWidth > document.documentElement.clientWidth,
}));
console.log("SETTINGS", JSON.stringify(set, null, 2));
await desk.screenshot({ path: ".dbg/resettings-desktop.png" });

// ---- Phone: dashboard + drawer with user header/logout ----
const phone = await browser.newPage({ viewport: { width: 390, height: 844 } });
await login(phone);
await phone.waitForTimeout(1500);
await phone.screenshot({ path: ".dbg/redash-phone.png" });
await phone.click(".mobile-nav-toggle");
await phone.waitForSelector(".drawer-head", { timeout: 5000 });
await phone.waitForTimeout(600);
const drawer = await phone.evaluate(() => ({
  avatarText: document.querySelector(".drawer-avatar")?.textContent?.trim(),
  userName: document.querySelector(".drawer-user strong")?.textContent?.trim(),
  roleLine: !!document.querySelector(".drawer-user span")?.textContent?.trim(),
  logoutVisible: (() => {
    const b = document.querySelector(".drawer-logout");
    if (!b) return false;
    const r = b.getBoundingClientRect();
    return r.width > 0 && r.height > 0;
  })(),
  navItems: document.querySelectorAll(".drawer-nav .nav-item").length,
  drawerBg: getComputedStyle(document.querySelector(".drawer-shell")).backgroundColor,
}));
console.log("DRAWER", JSON.stringify(drawer, null, 2));
await phone.screenshot({ path: ".dbg/re-drawer-phone.png" });

// Logout from the drawer should land back on /login.
await phone.click(".drawer-logout");
await phone.waitForTimeout(1500);
console.log("AFTER_LOGOUT_URL", phone.url());

await browser.close();
