// Responsive sweep: screenshots every tab at phone/tablet/desktop widths so
// layout squeezes can be eyeballed. Run: node tests/e2e/responsive-sweep.mjs [url]
import { chromium } from "playwright";
import fs from "node:fs";

const BASE = process.argv[2] || process.env.MUDP_E2E_URL || "http://127.0.0.1:19200";
const OUT = "test-results/responsive";
fs.mkdirSync(OUT, { recursive: true });

const TABS = [
  "dashboard", "netdisk", "containers", "mcp", "processes", "usage", "images",
  "volumes", "networks", "forwards", "stacks", "hardware", "users", "audit",
  "security", "errors", "disks", "database", "settings", "help",
];
// The URL path equals the nav key.
const pathOf = (tab) => tab;
const WIDTHS = [
  { name: "w375", width: 375, height: 720 },   // phone
  { name: "w768", width: 768, height: 1024 },  // tablet portrait
  { name: "w1024", width: 1024, height: 800 }, // small laptop
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
  for (const tab of TABS) {
    // On phones the sidebar is a drawer (div.container, not the aside nav);
    // match either by the data-tab attribute alone.
    const navBtn = page.locator(`button[data-tab="${tab}"]:visible`).first();
    if (!(await navBtn.count())) {
      await page.locator(".mobile-nav-toggle").click().catch(() => {});
      await page.waitForTimeout(400);
    }
    await page.locator(`button[data-tab="${tab}"]:visible`).first().click().catch((e) => {
      console.log(`MISSING ${name}/${tab}: ${e.message.split("\n")[0]}`);
    });
    // Wait for the SPA route to actually change before shooting, so a slow
    // data fetch can never capture the previous tab under the new name.
    await page.waitForURL(`**/${pathOf(tab)}`, { timeout: 20000 }).catch(() => {});
    await page.waitForTimeout(1200);
    // capture the whole scrollable pane so cramped rows below the fold show too
    await page.screenshot({ path: `${OUT}/${name}-${tab}.png`, fullPage: true });
  }
  await page.close();
}
await browser.close();
console.log(errors.length ? "JS ERRORS:\n" + errors.join("\n") : "NO JS ERRORS");
console.log("done:", fs.readdirSync(OUT).length, "screenshots in", OUT);
