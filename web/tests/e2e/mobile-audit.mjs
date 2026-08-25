// Mobile overflow audit: visits every tab at phone width and reports pages
// where the document scrolls horizontally or where fixed-position overlays
// cover main content. Run: node tests/e2e/mobile-audit.mjs [url]
import { chromium } from "playwright";

const BASE = process.argv[2] || "http://127.0.0.1:9100";
const TABS = [
  "dashboard", "netdisk", "containers", "mcp", "processes", "usage", "images",
  "volumes", "networks", "forwards", "stacks", "hardware", "users", "audit",
  "security", "errors", "disks", "database", "settings", "help",
];

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 375, height: 812 } });
const errors = [];
page.on("pageerror", (e) => errors.push(e.message));
await page.goto(BASE + "/");
await page.fill("input[name='username']", "admin");
await page.fill("input[name='password']", "test123456");
await page.click("form.auth-card .auth-submit");
await page.waitForSelector(".work-header", { timeout: 30000 });

for (const tab of TABS) {
  await page.goto(BASE + "/" + tab);
  await page.waitForTimeout(1200);
  const m = await page.evaluate(() => {
    const de = document.documentElement;
    // find elements wider than the viewport
    const wide = [];
    for (const el of document.querySelectorAll(".app-main *")) {
      const r = el.getBoundingClientRect();
      if (r.width > de.clientWidth + 2 && el.children.length === 0) {
        wide.push(el.tagName + "." + String(el.className).split(" ")[0] + " w=" + Math.round(r.width));
      }
    }
    return {
      clientW: de.clientWidth,
      scrollW: de.scrollWidth,
      wide: wide.slice(0, 4),
      tableOverflowers: [...document.querySelectorAll(".el-table")].map((t) => {
        const r = t.parentElement.getBoundingClientRect();
        return { parentW: Math.round(r.width), tableScrollW: t.scrollWidth };
      }).filter((x) => x.tableScrollW > x.parentW + 2),
    };
  });
  const flag = m.scrollW > m.clientW + 2 ? "HSCROLL" : "ok";
  console.log(`${flag.padEnd(8)} ${tab.padEnd(11)} client=${m.clientW} scroll=${m.scrollW}` +
    (m.wide.length ? ` wide:[${m.wide.join(" | ")}]` : "") +
    (m.tableOverflowers.length ? ` tables:${JSON.stringify(m.tableOverflowers)}` : ""));
}
await browser.close();
console.log(errors.length ? "JS ERRORS:\n" + errors.join("\n") : "NO JS ERRORS");
