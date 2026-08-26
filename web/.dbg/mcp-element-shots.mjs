// Element-scoped screenshots of the MCP tokens table (desktop) and the phone
// action sheet, for visual review of the icon actions column.
import { chromium } from "playwright";
import fs from "node:fs";
import { startServer, seed, apiClient } from "../tests/e2e/fixtures/server.js";

const OUT = "test-results/mcp-actions";
fs.mkdirSync(OUT, { recursive: true });

const server = await startServer({ port: 19312 });
let admin;
let seeded;
try {
  seeded = await seed(server, { runId: "mcpshot" });
  admin = await apiClient(server.url, server.adminUser, server.adminPassword);
  const containers = (await admin.get("/api/containers")) || [];
  const target = containers.find((c) => (c.name || "").includes("mcpshot")) || containers[0];
  for (const [label, hours] of [["demo-a", 0], ["demo-b", 24]]) {
    await admin.post("/api/mcp/tokens", { containerId: target.id, label, expiresInHours: hours });
  }
  await admin.post("/api/admin/mcp/remote", { enabled: true, port: 19322, domain: "mcp.example.com", safeNetwork: "bridge" });

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

  {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    await login(page);
    await page.goto(server.url + "/mcp");
    await page.waitForSelector(".mcp-page .el-table__row", { timeout: 20000 });
    await page.waitForTimeout(800);
    await page.locator(".mcp-page > .card").last().screenshot({ path: `${OUT}/element-tokens-table.png` });
    await page.close();
  }
  {
    const page = await browser.newPage({ viewport: { width: 375, height: 720 } });
    await login(page);
    await page.goto(server.url + "/mcp");
    await page.waitForSelector(".mcp-page .el-table__row", { timeout: 20000 });
    await page.waitForTimeout(800);
    await page.locator(".el-table__row").first().click();
    await page.waitForSelector(".action-sheet:visible", { timeout: 5000 });
    await page.locator(".action-sheet").screenshot({ path: `${OUT}/element-phone-sheet.png` });
    await page.close();
  }
  await browser.close();
  console.log("shots saved");
} finally {
  try { await seeded?.cleanup(); } catch { /* best effort */ }
  try { await admin?.dispose(); } catch { /* best effort */ }
  await server.stop();
}
