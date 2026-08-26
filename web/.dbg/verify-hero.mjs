// One-off check of the animated login hero: aurora blobs animating, glass
// pills, refreshed tagline copy, and the narrow-screen fallback.
// Run: node .dbg/verify-hero.mjs  (server on 127.0.0.1:19100)
import { chromium } from "playwright";
import crypto from "node:crypto";

const browser = await chromium.launch();

// ---- Desktop 1280 ----
const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
await page.goto("http://127.0.0.1:19100/login", { waitUntil: "domcontentloaded" });
await page.waitForSelector(".auth-hero", { timeout: 15000 });
await page.waitForTimeout(1500);

const hero = await page.evaluate(() => {
  const blobs = [...document.querySelectorAll(".hero-bg span")].map((b) => ({
    cls: b.className,
    anim: getComputedStyle(b).animationName,
    dur: getComputedStyle(b).animationDuration,
    blur: getComputedStyle(b).filter.slice(0, 12),
  }));
  return {
    blobs,
    pills: [...document.querySelectorAll(".hero-feats span")].map((p) => p.textContent),
    pillBg: getComputedStyle(document.querySelector(".hero-feats span")).backgroundColor,
    pillBlur: getComputedStyle(document.querySelector(".hero-feats span")).backdropFilter,
    iconBlur: getComputedStyle(document.querySelector(".app-icon")).backdropFilter,
    tagline: document.querySelector(".app-tagline")?.textContent?.slice(0, 60),
    heroOverflow: getComputedStyle(document.querySelector(".auth-hero")).overflow,
  };
});
console.log("HERO", JSON.stringify(hero, null, 2));

// Two screenshots 1.6s apart must differ — proof the animation is moving.
const a = await page.screenshot();
await page.waitForTimeout(1600);
const b = await page.screenshot();
const ha = crypto.createHash("md5").update(a).digest("hex");
const hb = crypto.createHash("md5").update(b).digest("hex");
console.log("ANIMATING", ha !== hb, ha.slice(0, 8), hb.slice(0, 8));
await page.screenshot({ path: ".dbg/hero-desktop.png" });

// ---- Phone 390 ----
const phone = await browser.newPage({ viewport: { width: 390, height: 844 } });
await phone.goto("http://127.0.0.1:19100/login", { waitUntil: "domcontentloaded" });
await phone.waitForSelector(".auth-hero", { timeout: 15000 });
await phone.waitForTimeout(800);
const m = await phone.evaluate(() => ({
  heroBgHidden: getComputedStyle(document.querySelector(".hero-bg")).display === "none",
  featsHidden: getComputedStyle(document.querySelector(".hero-feats")).display === "none",
  iconBg: getComputedStyle(document.querySelector(".login-brand .app-icon")).backgroundImage.slice(0, 40),
  nameColor: getComputedStyle(document.querySelector(".login-brand .app-name")).color,
  hScroll: document.documentElement.scrollWidth > document.documentElement.clientWidth,
}));
console.log("PHONE", JSON.stringify(m, null, 2));
await phone.screenshot({ path: ".dbg/hero-phone.png" });

await browser.close();
