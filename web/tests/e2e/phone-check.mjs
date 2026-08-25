// Deterministic phone/tablet layout shots: navigate by URL (no drawer
// interaction), wait for the header to match, and capture the .work pane.
// Run: node tests/e2e/phone-check.mjs [url]
import { chromium } from "playwright";
import fs from "node:fs";

const BASE = process.argv[2] || "http://127.0.0.1:19200";
const OUT = "test-results/phone";
fs.rmSync(OUT, { recursive: true, force: true });
fs.mkdirSync(OUT, { recursive: true });

// tab key → [route path, expected h1 (en locale)]
const TABS = [
  ["dashboard", "/dashboard", "Dashboard"],
  ["netdisk", "/netdisk", "Netdisk"],
  ["containers", "/containers", "Containers"],
  ["images", "/images", "Images"],
  ["forwards", "/forwards", "Forwards"],
  ["users", "/users", "Users"],
  ["security", "/security", "Security"],
  ["hardware", "/hardware", "Hardware"],
  ["usage", "/usage", "Usage"],
  ["settings", "/settings", "Settings"],
];

const WIDTHS = [
  { name: "p375", width: 375, height: 720 },
  { name: "t768", width: 768, height: 1024 },
];

const browser = await chromium.launch();
const errors = [];
for (const { name, width, height } of WIDTHS) {
  const page = await browser.newPage({ viewport: { width, height } });
  page.on("pageerror", (e) => errors.push(`${name}: ${e.message}`));
  await page.goto(BASE + "/");
  await page.fill("input[name='username']", "admin");
  await page.fill("input[name='password']", "smoke-secret-123");
  await page.click("form.auth-card .auth-submit");
  await page.waitForSelector(".work-header", { timeout: 60000 });
  for (const [key, path, h1] of TABS) {
    await page.goto(BASE + path);
    // The h1 comes from route meta, so it paints even while data loads.
    await page.locator(".work-header h1", { hasText: h1 }).waitFor({ state: "visible", timeoutMs: 15000 }).catch(() => {});
    await page.waitForTimeout(800);
    await page.locator(".work").screenshot({ path: `${OUT}/${name}-${key}.png` });
    console.log(`shot ${name}/${key} h1=${await page.locator(".work-header h1").textContent().catch(() => "?")}`);
  }
  await page.close();
}
await browser.close();
console.log(errors.length ? "JS ERRORS:\n" + errors.join("\n") : "NO JS ERRORS");
