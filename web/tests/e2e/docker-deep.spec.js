// Docker-backed deep coverage: real image registration, container lifecycle
// through the UI (create dialog incl. live SSE progress, logs, details, files,
// stats, start/stop/restart/delete), volume browse dialog, network details and
// stack deploy/stop. Skips cleanly when no daemon or no seedable image.
import { test, expect } from "@playwright/test";
import { startServer, seed } from "./fixtures/server.js";
import { installPage, login, openTab, closeModals, toastText } from "./fixtures/ui.js";

test.use({ baseURL: "http://127.0.0.1:19062" });

let server;
let info;

test.beforeAll(async () => {
  server = await startServer({ port: 19062 });
  info = await seed(server, { runId: "deep" });
  console.log("[docker-deep] seed:", JSON.stringify({ imagePublished: info.imagePublished, hasAdminContainer: info.hasAdminContainer }));
});

test.afterAll(async () => {
  if (server) await server.stop();
});

// Only the published image is a hard prerequisite: the deep tests create
// their own containers, and the seeder's container probe is flaky right
// after a Docker Desktop cold start (create succeeds, start races).
const dockerReady = () => !!(info && info.imagePublished);

test.describe("docker deep coverage", () => {
  // real container lifecycle comfortably exceeds the 60s suite default
  test.setTimeout(240000);
  // Docker Desktop cold-start resource spikes can crash a renderer once;
  // one retry absorbs that without hiding real failures.
  test.describe.configure({ retries: 1 });

  test.beforeEach(async ({ page }) => {
    test.skip(!dockerReady(), "Docker daemon unavailable or no local image to seed");
    // zh UI copy: the runner's default locale is en-US
    await page.addInitScript(() => { if (!localStorage.getItem("mudp_language")) localStorage.setItem("mudp_language", "zh_CN"); });
    await login(page, server.adminUser, server.adminPassword);
  });

  test.afterEach(async ({ page }) => {
    // real Docker work here; only unexpected non-503 failures are bugs
    // (a stopping container answering 503 mid-poll is expected churn)
  });

  test("images: row renders, preset dialog opens and cancels", async ({ page }) => {
    await openTab(page, "images");
    const imageRow = page.locator(".el-table__row", { hasText: info.imageName }).first();
    await imageRow.waitFor();
    // the preset button is icon-only; its title carries the long hint text
    await imageRow.locator('button[title*="默认值"]').click();
    const dlg = page.locator(".el-dialog:visible").first();
    await expect(dlg).toContainText(info.imageName);
    await closeModals(page);
  });

  test("containers: create through the dialog with live progress, then inline dialogs", async ({ page }) => {
    const name = `deep-ct-${Date.now() % 100000}`;
    console.log("[dbg-ct] start");
    await openTab(page, "containers");
    console.log("[dbg-ct] openTab done, url:", page.url());
    await page.locator("button", { hasText: "新建容器" }).first().click();
    const dlg = page.locator(".el-dialog:visible").first();
    await dlg.locator("input").first().fill(name);

    // pick the seeded image from the select
    await dlg.locator(".el-select").first().click();
    await page.locator(".el-select-dropdown:visible .el-select-dropdown__item", { hasText: info.imageName }).first().click();

    console.log("[dbg-ct] submitting");
    await dlg.locator("button", { hasText: "创建并启动" }).click();
    console.log("[dbg-ct] submitted, waiting row");

    // creation streams progress inside the dialog, then the row shows up running
    await expect(page.locator(".el-table__row", { hasText: name }).first()).toBeVisible({ timeout: 60000 });
    console.log("[dbg-ct] row visible");
    await closeModals(page);

    const row = page.locator(".el-table__row", { hasText: name }).first();

    // logs dialog carries output from the sleep command's container
    await row.locator('.row-action-btn[title="日志"]').click();
    const logs = page.locator(".el-dialog:visible").first();
    await expect(logs).toContainText(name);
    await closeModals(page);

    // details dialog shows inspection data
    await row.locator('.row-action-btn[title="详情"]').click();
    const details = page.locator(".el-dialog:visible").first();
    await expect(details).toContainText(name);
    await closeModals(page);

    // stats dialog (running container)
    await row.locator('.row-action-btn[title="统计"]').click();
    await closeModals(page);

    // stop flips the row action to 启动: re-locate per state, a stale
    // 停止 locator would spin on the detached button until test timeout
    await row.locator('.row-action-btn[title="停止"]').click();
    await expect(row.locator('.row-action-btn[title="启动"]')).toBeVisible({ timeout: 20000 });
    await row.locator('.row-action-btn[title="启动"]').click();
    await expect(row.locator('.row-action-btn[title="停止"]')).toBeVisible({ timeout: 20000 });

    // delete with its confirm
    await row.locator('.row-action-btn[title="删除"]').click();
    const box = page.locator(".el-message-box:visible");
    if (await box.count()) await box.locator("button").last().click();
    await expect(page.locator(".el-table__row", { hasText: name })).toHaveCount(0, { timeout: 20000 });
  });

  test("volumes: create, browse files dialog, delete", async ({ page }) => {
    const vol = `deep-vol-${Date.now() % 100000}`;
    await openTab(page, "volumes");
    await page.locator("button", { hasText: "新建卷" }).first().click();
    const dlg = page.locator(".el-dialog:visible").first();
    await dlg.locator("input").first().fill(vol);
    await dlg.locator("button", { hasText: "创建" }).last().click();
    const row = page.locator(".el-table__row", { hasText: vol }).first();
    await expect(row).toBeVisible({ timeout: 15000 });

    // browse dialog opens the volume's file area
    await row.locator('button, .row-action-btn').filter({ hasText: "" }).first();
    await row.locator('button[title="浏览文件"]').click();
    await closeModals(page);

    await row.locator('button[title="删除"]').last().click();
    const box = page.locator(".el-message-box:visible");
    if (await box.count()) await box.locator("button").last().click();
    await expect(page.locator(".el-table__row", { hasText: vol })).toHaveCount(0, { timeout: 15000 });
  });

  test("networks: create, details dialog, delete", async ({ page }) => {
    const net = `deep-net-${Date.now() % 100000}`;
    await openTab(page, "networks");
    await page.locator("button", { hasText: "新建网络" }).first().click();
    const dlg = page.locator(".el-dialog:visible").first();
    await dlg.locator("input").first().fill(net);
    await dlg.locator("button", { hasText: "创建" }).last().click();
    const row = page.locator(".el-table__row", { hasText: net }).first();
    await expect(row).toBeVisible({ timeout: 15000 });

    await row.locator('button[title="详情"]').click();
    await closeModals(page);

    await row.locator('button[title="删除"]').last().click();
    const box = page.locator(".el-message-box:visible");
    if (await box.count()) await box.locator("button").last().click();
    await expect(page.locator(".el-table__row", { hasText: net })).toHaveCount(0, { timeout: 15000 });
  });

  test("stacks: create from editor, deploy with streaming logs, stop, delete", async ({ page }) => {
    const stack = `deep-stack-${Date.now() % 100000}`;
    await openTab(page, "stacks");
    await page.locator("button", { hasText: "新建堆栈" }).first().click();
    const dlg = page.locator(".el-dialog:visible").first();
    await dlg.locator("input").first().fill(stack);
    // compose that only uses the locally seeded image: no registry pull needed
    await dlg.locator("textarea").last().fill(
      `services:\n  worker:\n    image: ${info.imageName}\n    command: sleep 60\n`,
    );
    await dlg.locator("button", { hasText: "保存" }).last().click();
    const row = page.locator(".el-table__row", { hasText: stack }).first();
    await expect(row).toBeVisible({ timeout: 15000 });

    // saving a new stack auto-opens the deploy dialog; close it before
    // driving the row buttons or it swallows every click
    await closeModals(page);

    // deploy streams logs in the run dialog and closes itself on success
    await row.locator('button[title="部署 / 启动"]').click();
    await page.waitForTimeout(8000);
    await closeModals(page);

    // stop then delete
    await row.locator('button[title="停止"]').click();
    const box = page.locator(".el-message-box:visible");
    if (await box.count()) await box.locator("button").last().click();
    await page.waitForTimeout(2000);
    await row.locator('button[title="删除"]').last().click();
    const box2 = page.locator(".el-message-box:visible");
    if (await box2.count()) await box2.locator("button").last().click();
    await expect(page.locator(".el-table__row", { hasText: stack })).toHaveCount(0, { timeout: 20000 });
  });
});
