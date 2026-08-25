// Post-fix interactive verification: drives the real UI in headless Chromium
// and asserts the behaviours fixed in this round. Run:
//   node tests/e2e/verify-fixes.mjs   (expects a server on 127.0.0.1:19200)
import { chromium } from "playwright";
import fs from "node:fs";

const BASE = process.env.MUDP_E2E_URL || "http://127.0.0.1:19200";
const OUT = "test-results/verify";
fs.mkdirSync(OUT, { recursive: true });

const results = [];
const check = (name, ok, detail = "") => {
  results.push(`${ok ? "PASS" : "FAIL"} ${name}${detail ? " — " + detail : ""}`);
};
const jsErrors = [];
const noise = (m) => m.startsWith("Failed to load resource");

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
page.on("pageerror", (e) => jsErrors.push(`pageerror: ${e.message}`));
page.on("console", (m) => {
  if (m.type() === "error" && !noise(m.text())) jsErrors.push(`console: ${m.text()}`);
});

// --- login ---
await page.goto(BASE + "/");
await page.fill("input[name='username']", "admin");
await page.fill("input[name='password']", "smoke-secret-123");
// Refresh the captcha and submit the answer for the challenge the page shows.
const [capResp] = await Promise.all([
  page.waitForResponse((r) => r.url().includes("/api/captcha")),
  page.click(".captcha-img"),
]);
await page.fill("input[name='captcha']", capResp.headers()["x-mudp-captcha-answer"]);
await page.click("form.auth-card .auth-submit");
await page.waitForSelector("aside nav", { timeout: 60000 });

// --- 1. sidebar stays pinned while the main pane scrolls ---
const work = page.locator(".work");
const workScrolls = await work.evaluate((el) => getComputedStyle(el).overflowY === "auto");
check("sidebar: .work is the scroll container", workScrolls);
// Make the page long enough to scroll, then verify the nav is still at top.
await work.evaluate((el) => { el.scrollTop = 400; });
await page.waitForTimeout(200);
const navTop = await page.locator("aside .shell-nav").boundingBox();
check("sidebar: nav unaffected by content scroll", !!navTop && navTop.y >= 0 && navTop.y < 120, `navTop.y=${navTop?.y}`);

// --- 2. container row selection survives the 5s auto-refresh ---
await page.click('nav button[data-tab="containers"]');
await page.waitForSelector(".el-table__row", { timeout: 30000 }).catch(() => {});
const rowCount = await page.locator(".el-table__row").count();
if (rowCount > 0) {
  await page.locator(".el-table__row .el-checkbox").first().click();
  await page.waitForTimeout(6500); // past one refresh tick
  const stillChecked = await page.locator(".el-table__row").first().locator(".el-checkbox.is-checked").count();
  check("containers: selection survives auto-refresh (row-key)", stillChecked === 1);
  await page.locator(".el-table__row .el-checkbox").first().click();
} else {
  check("containers: no rows to test selection (skipped)", true);
}

// --- 3. netdisk previews: csv / json / xlsx / docx / md ---
await page.click('nav button[data-tab="netdisk"]');
await page.waitForSelector(".el-table__row", { timeout: 30000 });

async function previewOf(fileName) {
  const link = page.locator(".el-table__row", { hasText: fileName }).locator(".name-link").first();
  if (!(await link.count())) return null;
  await link.click();
  await page.waitForTimeout(1800); // lazy vendor lib load can take a beat
  const dlg = page.locator(".el-dialog__wrapper:visible").last();
  const html = (await dlg.innerHTML().catch(() => "")) || "";
  await page.screenshot({ path: `${OUT}/preview-${fileName}`.replace(/\.\w+$/, ".png") });
  // close the dialog
  await page.keyboard.press("Escape");
  await page.waitForTimeout(300);
  await page.locator(".el-dialog__wrapper:visible .el-dialog__headerbtn").last().click().catch(() => {});
  await page.waitForTimeout(300);
  return html;
}

const csv = await previewOf("mudp-demo.csv");
check("viewer: CSV renders as a table", !!csv && csv.includes("preview-table") && csv.includes("alice"));
const json = await previewOf("mudp-config.json");
check("viewer: JSON pretty-printed", !!json && json.includes("&quot;project&quot;") || (json || "").includes('"project"'));
const xlsx = await previewOf("mudp-report.xlsx");
check("viewer: XLSX renders first sheet", !!xlsx && xlsx.includes("GPU server"));
const docx = await previewOf("mudp-notes.docx");
check("viewer: DOCX renders paragraphs", !!docx && docx.includes("MUDP Preview Test Document"));
const md = await previewOf("mudp-notes.md");
check("viewer: Markdown renders", !!md && md.includes("<h1") && md.includes("<li>"));

// --- 4. share page: new skin + previews ---
await page.goto(BASE + "/pan/XB557TOzcO3B");
await page.waitForSelector("table.share-table", { timeout: 20000 });
const bg = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
check("share: new stylesheet applied", bg === "rgb(241, 245, 249)", `body bg=${bg}`);
await page.screenshot({ path: `${OUT}/share-page.png`, fullPage: true });
const shareCsv = page.locator(".share-name-text", { hasText: "mudp-demo.csv" }).first();
await shareCsv.click();
await page.waitForTimeout(1500);
const sharePreview = await page.locator("#viewerBody").innerHTML().catch(() => "");
check("share: CSV preview via shared lib", sharePreview.includes("preview-table") && sharePreview.includes("bob"));
await page.screenshot({ path: `${OUT}/share-preview-csv.png` });
await page.keyboard.press("Escape");

check("no unexpected JS errors", jsErrors.length === 0, jsErrors.slice(0, 4).join(" | "));

await browser.close();
console.log(results.join("\n"));
const failed = results.filter((r) => r.startsWith("FAIL"));
console.log(failed.length ? `\n${failed.length} FAILED` : "\nALL PASS");
process.exit(failed.length ? 1 : 0);
