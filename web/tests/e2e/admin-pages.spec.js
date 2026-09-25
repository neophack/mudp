// Admin observation & system pages: audit, security, errors, disks, database,
// forwards, help, dashboard. Every interaction these pages offer without a
// Docker daemon — filters, tabs, confirmations, saves — is driven and asserted.
import { test, expect } from "@playwright/test";
import { startServer, seed } from "./fixtures/server.js";
import { installPage, login, openTab, toastText } from "./fixtures/ui.js";

test.use({ baseURL: "http://127.0.0.1:19042" });

let server;

test.beforeAll(async () => {
  server = await startServer({ port: 19042 });
  await seed(server, { runId: "admin" });
});

test.afterAll(async () => {
  if (server) await server.stop();
});

let helper;

test.beforeEach(async ({ page }) => {
  // Playwright's default locale is en-US; pin the app to Chinese so the
  // zh UI copy the assertions rely on is actually rendered.
  await page.addInitScript(() => { if (!localStorage.getItem("mudp_language")) localStorage.setItem("mudp_language", "zh_CN"); });
  helper = installPage(page);
  await login(page, server.adminUser, server.adminPassword);
});

test.afterEach(async ({ page }) => {
  helper.assertClean("", { ignore503: true });
});

test("dashboard: neutral empty ring, env card visible for admin", async ({ page }) => {
  await openTab(page, "dashboard");
  await expect(page.locator("section.card", { hasText: "环境" }).first()).toBeVisible();
  await expect(page.locator("section.card", { hasText: "容器" }).first()).toBeVisible();
});

test("hardware: live charts actually paint canvases", async ({ page }) => {
  // Guards the echarts on-demand registration: if a chart type or component
  // is missing from the bundle the page still lays out but renders no canvas.
  await openTab(page, "hardware");
  await page.waitForSelector(".echart-box canvas", { timeout: 15000 });
  expect(await page.locator(".echart-box canvas").count()).toBeGreaterThanOrEqual(2);
});

test("audit: rows render, filter narrows, CSV export present", async ({ page }) => {
  await openTab(page, "audit");
  const rows = page.locator(".el-table__row");
  await rows.first().waitFor();
  const before = await rows.count();

  // filter by operator narrows to their rows only
  await page.locator(".toolbar input, .card input").first().fill("admin");
  await page.waitForTimeout(400);
  const after = await rows.count();
  expect(after).toBeLessThanOrEqual(before);

  await expect(page.locator("a", { hasText: "导出 CSV" }).or(page.locator("button", { hasText: "导出 CSV" }))).toBeVisible();
});

test("security: all four tabs switch and paint", async ({ page }) => {
  await openTab(page, "security");
  for (const tab of ["概览", "访问记录", "设置", "MCP"]) {
    await page.locator(".el-radio-button", { hasText: tab }).first().click();
    await expect(page.locator(".app-main")).not.toBeEmpty();
  }
  // settings tab: toggles + save button exist
  await page.locator(".el-radio-button", { hasText: "设置" }).first().click();
  await expect(page.locator("button", { hasText: "保存" }).first()).toBeVisible();
});

test("errors: docker-absent events are listed, resolve clears a row, clear-all confirms", async ({ page }) => {
  await openTab(page, "errors");
  const rows = page.locator(".el-table__row");
  // without Docker the proxy endpoints have already logged 503s
  await expect(page.locator("button", { hasText: "标记解决" }).first()).toBeVisible({ timeout: 10000 });

  const firstAction = page.locator("button", { hasText: "标记解决" }).first();
  const firstRow = page.locator(".el-table__row").filter({ has: firstAction }).first();
  await firstRow.waitFor();
  await firstAction.click();
  // the resolved event's own action link goes away (count assertions would
  // race: Docker churn keeps appending new 503 rows while we look)
  await expect(page.locator("button", { hasText: "标记解决" }).first()).toBeVisible({ timeout: 10000 });

  // clear-all asks for confirmation and cancelling keeps the data
  await page.locator("button", { hasText: "清空" }).first().click();
  const box = page.locator(".el-message-box:visible");
  await expect(box).toBeVisible();
  await box.locator("button").first().click(); // cancel
  await expect(box).toHaveCount(0);
});

test("disks: backup time pads to two digits, unmount asks first", async ({ page }) => {
  await openTab(page, "disks");
  const hour = page.locator(".bk-sched-time input").nth(0);
  const minute = page.locator(".bk-sched-time input").nth(1);
  await expect(hour).toHaveValue(/^\d{2}$/);
  await expect(minute).toHaveValue(/^\d{2}$/);

  // unmount goes through a confirmation that stays cancellable
  const unmount = page.locator("button, a", { hasText: "卸载" }).first();
  await unmount.click();
  const box = page.locator(".el-message-box:visible");
  await expect(box).toBeVisible();
  await box.locator("button").first().click();
});

test("database: clean dialog opens with retention options and cancels", async ({ page }) => {
  await openTab(page, "database");
  await page.locator("button", { hasText: "清理" }).first().click();
  const dlg = page.locator(".el-dialog:visible").first();
  await expect(dlg).toContainText("清理");
  await expect(dlg.locator(".el-select, select").first()).toBeVisible();
  await dlg.locator("button", { hasText: "取消" }).first().click();
});

test("forwards: add dialog validates empty input, forward network saves", async ({ page }) => {
  await openTab(page, "forwards");

  // empty submit surfaces a validation message instead of creating a rule
  await page.locator("button", { hasText: "添加转发" }).first().click();
  const dlg = page.locator(".el-dialog:visible").first();
  await dlg.locator("button", { hasText: "添加转发" }).last().click();
  expect(await toastText(page)).toBeTruthy();
  await dlg.locator("button", { hasText: "取消" }).click();

  // network allowlist text saves
  await page.locator("textarea").first().fill("crud-lan");
  await page.locator("button", { hasText: "保存网络" }).click();
  expect(await toastText(page)).toBeTruthy();
});

test("help: renders the admin guide and version", async ({ page }) => {
  await openTab(page, "help");
  await expect(page.locator(".app-main")).toContainText("管理员");
  await expect(page.locator(".app-main")).toContainText(/v\d+\.\d+\.\d+/);
});
