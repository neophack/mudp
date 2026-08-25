// Visual smoke: logs into the running server and screenshots the key pages so
// the refactor can be eyeballed. Run: node tests/e2e/visual-smoke.mjs [url]
import { chromium } from "playwright";

const BASE = process.argv[2] || "http://127.0.0.1:19200";
const OUT = "test-results/visual";

const pages = [
  "dashboard", "containers", "images", "volumes", "networks", "stacks",
  "netdisk", "mcp", "processes", "usage", "hardware", "users", "audit",
  "security", "errors", "disks", "database", "settings", "help", "forwards",
];

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
const errors = [];
page.on("pageerror", (e) => errors.push(`${page.url()}: ${e.message}`));
// "Failed to load resource" console lines are 4xx responses the app handles
// (e.g. /api/netdisk/quota 400s when the group has no netdisk path configured)
// — the browser logs them regardless of any JS catch, so only collect the rest.
page.on("console", (m) => {
  if (m.type() === "error" && !m.text().startsWith("Failed to load resource")) errors.push(`${page.url()}: console ${m.text()}`);
});

await page.goto(BASE);
await page.waitForSelector("form.auth-card");
await page.screenshot({ path: `${OUT}/00-login.png` });
await page.fill("input[name='username']", "admin");
await page.fill("input[name='password']", "smoke-secret-123");
await page.click("form.auth-card .auth-submit");
await page.waitForSelector("aside nav", { timeout: 60000 });

for (const tab of pages) {
  await page.click(`nav button[data-tab="${tab}"]`).catch(async (e) => {
    console.log(`MISSING TAB: ${tab} (${e.message.split("\n")[0]})`);
  });
  await page.waitForTimeout(1800);
  await page.screenshot({ path: `${OUT}/${tab}.png` });
  console.log(`shot ${tab}`);
}

// Security sub-tabs (map lives in overview; also check logs/settings/mcp)
await page.click(`nav button[data-tab="security"]`);
await page.waitForTimeout(2500);
await page.screenshot({ path: `${OUT}/security-overview.png`, fullPage: true });
for (const label of ["Access Log", "Settings", "MCP Security"]) {
  await page.click(`.el-radio-button:has-text("${label}")`).catch(() => {});
  await page.waitForTimeout(2000);
  await page.screenshot({ path: `${OUT}/security-${label.toLowerCase().replace(/ /g, "-")}.png`, fullPage: true });
}

console.log(errors.length ? "JS ERRORS:\n" + errors.join("\n") : "NO JS ERRORS");
await browser.close();
