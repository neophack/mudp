// MCP-page actions verification: the desktop actions column renders every
// action as an icon on ONE line (sized to the widest row), and phone width
// collapses the column into the bottom action sheet with the hidden columns'
// info as meta lines.
// Run: node tests/e2e/mcp-actions-check.mjs
import { chromium } from "playwright";
import fs from "node:fs";
import { startServer, seed, apiClient } from "./fixtures/server.js";

const PORT = 19310;
const OUT = "test-results/mcp-actions";
fs.mkdirSync(OUT, { recursive: true });

const results = [];
const check = (name, ok, detail = "") => {
  results.push(`${ok ? "PASS" : "FAIL"} ${name}${detail ? ` — ${detail}` : ""}`);
};

// True when any element spills past the right edge of its scroll container,
// ignoring Element's own intentional table scrollers.
async function overflowInfo(page) {
  return page.evaluate(() => {
    const doc = document.documentElement;
    const bodySpill = doc.scrollWidth - doc.clientWidth;
    let clipped = 0;
    const offenders = [];
    for (const el of document.querySelectorAll(".app-main *")) {
      if (el.closest(".el-table")) continue;
      const r = el.getBoundingClientRect();
      if (r.width > 0 && r.right > doc.clientWidth + 1) {
        clipped++;
        if (offenders.length < 5) offenders.push(el.className || el.tagName);
      }
    }
    return { bodySpill, clipped, offenders };
  });
}

const server = await startServer({ port: PORT });
let admin;
let seeded;
try {
  seeded = await seed(server, { runId: "mcpact" });
  admin = await apiClient(server.url, server.adminUser, server.adminPassword);

  const containers = (await admin.get("/api/containers")) || [];
  const target = containers.find((c) => (c.name || "").includes("mcpact")) || containers[0];
  if (!target) throw new Error("no container available for MCP tokens");

  // Two tokens, then publish the remote domain with the safe network set to
  // "bridge" (the seeded containers sit on it) — the worst case of five row
  // actions: view config / usage log / copy external / generate key / delete.
  for (const [label, hours] of [["demo-a", 0], ["demo-b", 24]]) {
    const r = await admin.post("/api/mcp/tokens", { containerId: target.id, label, expiresInHours: hours });
    if (!r.ok) throw new Error(`token create failed: ${r.body}`);
  }
  const remote = await admin.post("/api/admin/mcp/remote", {
    enabled: true, port: 19321, domain: "mcp.example.com", safeNetwork: "bridge",
  });
  if (!remote.ok) throw new Error(`remote enable failed: ${remote.body}`);

  const browser = await chromium.launch();

  async function login(page) {
    await page.goto(server.url + "/");
    await page.fill("input[name='username']", server.adminUser);
    await page.fill("input[name='password']", server.adminPassword);
    const [capResp] = await Promise.all([
      page.waitForResponse((r) => r.url().includes("/api/captcha")),
      page.click(".captcha-img"),
    ]);
    await page.fill("input[name='captcha']", capResp.headers()["x-mudp-captcha-answer"]);
    await page.click("form.auth-card .auth-submit");
    await page.waitForSelector(".work-header", { timeout: 60000 });
  }

  // ---------- Desktop ----------
  {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await login(page);
    await page.goto(server.url + "/mcp");
    await page.waitForSelector(".mcp-page .el-table__row", { timeout: 20000 });
    await page.waitForTimeout(1000);

    const remoteCard = ((await page.locator(".mcp-copy-row code").first().textContent()) || "").trim();
    check("remote baseUrl shown", remoteCard.includes("mcp.example.com"), remoteCard);

    const rows = await page.locator(".el-table__row").count();
    check("token rows rendered", rows >= 2, `rows: ${rows}`);
    const btns = await page.locator(".el-table__row .row-actions .el-button").count();
    check("five icon actions per row", btns === rows * 5, `${btns} buttons / ${rows} rows`);

    // The fixed column is sized from the widest row: icons neither wrap onto a
    // second line nor get clipped.
    const fit = await page.evaluate(() => {
      const out = { clipped: 0, wrapped: 0, colWidth: 0, rows: 0 };
      for (const el of document.querySelectorAll(".row-actions")) {
        out.rows++;
        if (el.scrollWidth > el.clientWidth + 1) out.clipped++;
        if (el.scrollHeight > 27) out.wrapped++;
      }
      const th = [...document.querySelectorAll(".el-table__header th")]
        .find((t) => (t.textContent || "").trim() === "Actions");
      if (th) out.colWidth = Math.round(th.getBoundingClientRect().width);
      return out;
    });
    check("actions stay on one line (desktop)", fit.clipped === 0 && fit.wrapped === 0, JSON.stringify(fit));
    check("actions column sized to widest row", fit.colWidth === 5 * 26 + 4 * 2 + 24, `width ${fit.colWidth}`);

    const aria = await page.locator(".el-table__row .row-actions .el-button").first().getAttribute("aria-label");
    check("icon actions carry aria-labels", aria === "View config", `first: ${aria}`);

    await page.screenshot({ path: `${OUT}/desktop-actions.png`, fullPage: true });

    // Clicking the first icon opens the config dialog.
    await page.locator(".el-table__row .row-actions .el-button").first().click();
    await page.waitForSelector(".el-dialog:visible", { timeout: 5000 });
    const sections = await page.locator(".el-dialog:visible .mcp-config-section").count();
    check("icon click opens config dialog", sections >= 1, `sections: ${sections}`);
    await page.screenshot({ path: `${OUT}/desktop-config-dialog.png` });
    await page.keyboard.press("Escape");
    await page.waitForTimeout(400);

    const ov = await overflowInfo(page);
    check("no horizontal overflow (desktop)", ov.bodySpill <= 0 && ov.clipped === 0, JSON.stringify(ov));
    check("no JS errors (desktop)", errors.length === 0, errors.join(" | "));
    await page.close();
  }

  // ---------- Phone ----------
  {
    const page = await browser.newPage({ viewport: { width: 375, height: 720 } });
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await login(page);
    await page.goto(server.url + "/mcp");
    await page.waitForSelector(".mcp-page .el-table__row", { timeout: 20000 });
    await page.waitForTimeout(800);

    const actionsHeaders = await page.locator(".el-table__header th:has-text('Actions')").count();
    check("phone hides actions column", actionsHeaders === 0, `found ${actionsHeaders}`);
    const iconCols = await page.locator(".el-table__row .row-actions").count();
    check("phone renders no icon buttons", iconCols === 0, `found ${iconCols}`);

    await page.locator(".el-table__row").first().click();
    await page.waitForSelector(".action-sheet:visible", { timeout: 5000 });
    const sheetBtns = await page.locator(".action-sheet .sheet-btn:visible").count();
    check("sheet lists all five actions", sheetBtns === 5, `buttons: ${sheetBtns}`);
    const metaLines = await page.locator(".action-sheet .sheet-meta-line").count();
    check("sheet folds in owner/created/last-used", metaLines === 3, `meta lines: ${metaLines}`);
    await page.screenshot({ path: `${OUT}/phone-sheet.png` });

    await page.locator(".action-sheet .sheet-btn:visible").first().click();
    await page.waitForSelector(".el-dialog:visible", { timeout: 5000 });
    const sections = await page.locator(".el-dialog:visible .mcp-config-section").count();
    check("sheet 'view config' opens config dialog", sections >= 1, `sections: ${sections}`);
    await page.screenshot({ path: `${OUT}/phone-config-dialog.png` });
    await page.keyboard.press("Escape");
    await page.waitForTimeout(400);

    const ov = await overflowInfo(page);
    check("no horizontal overflow (phone)", ov.bodySpill <= 0 && ov.clipped === 0, JSON.stringify(ov));
    await page.screenshot({ path: `${OUT}/phone-light.png`, fullPage: true });
    check("no JS errors (phone)", errors.length === 0, errors.join(" | "));
    await page.close();
  }

  await browser.close();
} finally {
  try { await seeded?.cleanup(); } catch { /* best effort */ }
  try { await admin?.dispose(); } catch { /* best effort */ }
  await server.stop();
}

const failed = results.filter((r) => r.startsWith("FAIL"));
for (const r of results) console.log(r);
console.log(failed.length ? `\n${failed.length} FAILURES` : "\nALL CHECKS PASSED");
process.exit(failed.length ? 1 : 0);
