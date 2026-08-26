// One-off check of the iOS-styled main shell against a throwaway server.
// Run: node .dbg/verify-shell.mjs  (server on 127.0.0.1:19100, admin/e2e-secret)
import { chromium } from "playwright";

const BASE = "http://127.0.0.1:19100";
const browser = await chromium.launch();

// --- Desktop shell ---
const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
await page.goto(BASE + "/login", { waitUntil: "domcontentloaded" });
await page.waitForSelector("form.auth-card");
await page.fill("input[name='username']", "admin");
await page.fill("input[name='password']", "e2e-secret");
await page.click("form.auth-card .auth-submit");
await page.waitForSelector(".work-header", { timeout: 30000 });
await page.waitForTimeout(2500);

const shell = await page.evaluate(() => {
  const cs = (sel, props) => {
    const el = document.querySelector(sel);
    if (!el) return null;
    const s = getComputedStyle(el);
    const out = {};
    for (const p of props) out[p] = s[p];
    return out;
  };
  return {
    aside: cs(".shell-aside", ["backgroundColor", "borderRightColor", "width"]),
    activeNav: cs(".shell-nav .nav-item.active", ["backgroundColor", "color", "borderRadius"]),
    h1: cs(".work-header h1", ["fontSize", "fontWeight"]),
    refreshBtn: cs(".ghost-btn", ["backgroundColor", "color", "borderTopWidth"]),
    firstCard: cs(".app-main .card", ["borderRadius", "borderTopWidth", "backgroundColor"]),
    bodyBg: cs("body", ["backgroundColor"]),
    sidebarHit: (() => {
      const el = document.elementFromPoint(60, 400);
      for (let n = el; n; n = n.parentElement) if (n.classList?.contains("shell-aside")) return true;
      return false;
    })(),
    hScroll: document.documentElement.scrollWidth > document.documentElement.clientWidth,
  };
});
console.log("SHELL", JSON.stringify(shell, null, 2));
await page.screenshot({ path: ".dbg/shell-desktop.png" });

// --- Phone shell ---
const mobile = await browser.newPage({ viewport: { width: 390, height: 844 } });
await mobile.goto(BASE + "/", { waitUntil: "domcontentloaded" });
await mobile.waitForSelector("form.auth-card");
await mobile.fill("input[name='username']", "admin");
await mobile.fill("input[name='password']", "e2e-secret");
await mobile.click("form.auth-card .auth-submit");
await mobile.waitForSelector(".work-header", { timeout: 30000 });
await mobile.waitForTimeout(1500);
const mShell = await mobile.evaluate(() => {
  const aside = document.querySelector(".shell-aside");
  const toggle = document.querySelector(".mobile-nav-toggle");
  return {
    asideHidden: aside ? getComputedStyle(aside).display === "none" : null,
    hamburgerVisible: toggle ? getComputedStyle(toggle).display !== "none" : null,
    hScroll: document.documentElement.scrollWidth > document.documentElement.clientWidth,
  };
});
console.log("PHONE", JSON.stringify(mShell, null, 2));
await mobile.screenshot({ path: ".dbg/shell-phone.png" });

await browser.close();
