// Full click-through of the console as a plain "user" role account: which tabs
// exist, what each page offers, the CRUD a user is allowed to perform, and the
// admin surface that must stay out of reach (in the UI *and* in the API).
//
// Same safety model as the admin spec: native confirm()/prompt() dialogs are
// cancelled unless a test opts in via withConfirm().

import { test, expect } from "@playwright/test";
import { startServer, seed } from "./fixtures/server.js";
import {
  USER_TABS, ADMIN_ONLY_TABS, CRAWLER_SKIP_BUTTONS, installPage, login, logout,
  openTab, closeModals, clickEveryButton, toastText,
} from "./fixtures/ui.js";

const PORT = 19101;
test.use({ baseURL: `http://127.0.0.1:${PORT}` });

test.describe.configure({ mode: "serial" });

let server;
let fixture;
let account;

test.beforeAll(async () => {
  server = await startServer({ port: PORT });
  fixture = await seed(server);
  account = fixture.user;
});

test.afterAll(async () => {
  if (fixture) await fixture.cleanup();
  if (server) await server.stop();
});

test("a plain user sees only the non-admin tabs", async ({ page }) => {
  const h = installPage(page);
  await login(page, account.username, account.password);

  const keys = await page.$$eval("nav button[data-tab]", (els) => els.map((e) => e.dataset.tab));
  expect(keys).toEqual(USER_TABS);
  for (const tab of ADMIN_ONLY_TABS) {
    await expect(page.locator(`nav button[data-tab="${tab}"]`), `${tab} must be hidden`).toHaveCount(0);
  }
  // The profile block shows the role the account actually has.
  await expect(page.locator("aside .profile")).toContainText("user");

  h.assertClean("the user sidebar");
});

test("every tab a user can reach renders", async ({ page }) => {
  test.setTimeout(180000);
  const h = installPage(page);
  await login(page, account.username, account.password);

  for (const tab of USER_TABS) {
    await openTab(page, tab);
    await expect(page.locator("header .titles h1"), `heading for ${tab}`).not.toBeEmpty();
    await expect(page.locator("#view"), `body of ${tab}`).not.toBeEmpty();
    h.assertClean(`the ${tab} tab`);
  }
});

test("header controls work for a user too", async ({ page }) => {
  const h = installPage(page);
  await login(page, account.username, account.password);

  await page.click("#refresh");
  expect(await toastText(page)).not.toBe("");

  await page.click("#sidebarToggle");
  await expect(page.locator("aside")).toHaveClass(/collapsed/);
  await page.click("#sidebarToggle");

  await page.click("#notificationBell");
  await expect(page.locator(".modal-backdrop.notifications-modal")).toBeVisible();
  await closeModals(page);

  await page.click("#jobsButton");
  await expect(page.locator(".modal-backdrop.jobs-modal")).toBeVisible();
  await closeModals(page);

  h.assertClean("the user header controls");
});

test("containers: a user sees only their own and can drive every row action", async ({ page }) => {
  test.setTimeout(180000);
  test.skip(!fixture.hasUserContainer, "Docker did not provide a container for the user account");
  const h = installPage(page);
  await login(page, account.username, account.password);
  await openTab(page, "containers");

  // The admin's seeded container must not be visible here, and there is no
  // Owner column for non-admins.
  const rows = page.locator("#view tbody tr");
  await expect(rows).toHaveCount(1);
  await expect(rows.first()).toContainText(`e2e-user-${fixture.runId}`);
  await expect(page.locator("#view thead th", { hasText: "Owner" })).toHaveCount(0);

  for (const f of ["running", "stopped", "paused", "all"]) {
    await page.click(`[data-filter="${f}"]`);
    await expect(page.locator(`[data-filter="${f}"]`)).toHaveClass(/active/);
  }

  await page.check("#selectAll");
  await expect(page.locator(".batch-bar")).toBeVisible();
  await page.click('[data-batch="remove"]');
  await page.waitForTimeout(300);
  // The confirmation was cancelled, so the container survives.
  await expect(page.locator("#view tbody tr")).toHaveCount(1);
  await page.uncheck("#selectAll");

  for (const [act, selector] of [
    ["logs", ".logs-modal"],
    ["inspect", ".detail-modal"],
    ["files", ".files-modal"],
    ["stats", ".stats-modal"],
    ["terminal", ".terminal-modal"],
  ]) {
    const btn = page.locator(`#view button[data-act="${act}"]`).first();
    if ((await btn.count()) === 0) continue;
    await btn.click();
    await expect(page.locator(`.modal-backdrop${selector}`), `${act} panel`).toBeVisible({ timeout: 20000 });
    await closeModals(page);
  }

  h.assertClean("the user containers page");
});

test("containers: a user can open the New Container wizard with the images published to them", async ({ page }) => {
  const h = installPage(page);
  await login(page, account.username, account.password);
  await openTab(page, "containers");

  await page.click("#newContainerBtn");
  await expect(page.locator("#newContainer")).toBeVisible();
  if (fixture.imagePublished) {
    await expect(page.locator("#newContainer [name='image'] option", { hasText: fixture.imageName })).toHaveCount(1);
  }
  await closeModals(page);

  h.assertClean("the user create-container wizard");
});

test("images: a user can browse the catalog but gets no lifecycle buttons", async ({ page }) => {
  const h = installPage(page);
  await login(page, account.username, account.password);
  await openTab(page, "images");

  await expect(page.locator("#view table.data")).toBeVisible();
  for (const id of ["#pullImageBtn", "#buildImageBtn", "#importImageBtn", "#registerImageBtn"]) {
    await expect(page.locator(id), `${id} is admin-only`).toHaveCount(0);
  }
  await expect(page.locator("#view button[data-image-delete]")).toHaveCount(0);
  await expect(page.locator("#view button[data-image-preset]")).toHaveCount(0);
  // Non-admins do not get the "Visible to" column.
  await expect(page.locator("#view thead th", { hasText: "Visible to" })).toHaveCount(0);

  h.assertClean("the user images page");
});

test("volumes: a user can create, browse and delete their own volume", async ({ page }) => {
  test.setTimeout(120000);
  const h = installPage(page);
  await login(page, account.username, account.password);
  await openTab(page, "volumes");

  const name = `ui-uvol-${fixture.runId}`;
  await page.click("#newVolumeBtn");
  await page.fill("#volForm [name='name']", name);
  await page.click("#volSubmit");
  const row = page.locator("#view tbody tr", { hasText: name });
  await expect(row).toBeVisible({ timeout: 30000 });

  await row.locator("button[data-vol-files]").click();
  await expect(page.locator(".modal-backdrop.volfiles-modal")).toBeVisible({ timeout: 20000 });
  await closeModals(page);

  await h.withConfirm(async () => {
    await row.locator("button.danger").click();
    await expect(page.locator("#view tbody tr", { hasText: name })).toHaveCount(0, { timeout: 30000 });
  });

  h.assertClean("the user volumes page");
});

test("networks: a user sees the list but cannot create one", async ({ page }) => {
  const h = installPage(page);
  await login(page, account.username, account.password);
  await openTab(page, "networks");

  await expect(page.locator("#view table.data")).toBeVisible();
  await expect(page.locator("#newNetBtn"), "network creation is admin-only").toHaveCount(0);

  // Details still opens for the networks they can see.
  const details = page.locator('#view button[title="Details"]').first();
  if ((await details.count()) > 0) {
    await details.click();
    await expect(page.locator(".modal-backdrop.network-modal")).toBeVisible({ timeout: 20000 });
    await closeModals(page);
  }

  h.assertClean("the user networks page");
});

test("stacks: a user can open the compose editor", async ({ page }) => {
  const h = installPage(page);
  await login(page, account.username, account.password);
  await openTab(page, "stacks");

  await page.click("#newStackBtn");
  await expect(page.locator("#stackForm [name='composeYaml']")).toContainText("services:");
  await closeModals(page);

  h.assertClean("the user stacks page");
});

test("netdisk: a user gets their own root and can manage folders in it", async ({ page }) => {
  test.setTimeout(180000);
  const h = installPage(page);
  await login(page, account.username, account.password);
  await openTab(page, "netdisk");

  // A user never sees the admin-wide share table.
  await expect(page.locator("#view", { hasText: "All External Links" })).toHaveCount(0);
  await expect(page.locator("#deleteSelectedShares")).toHaveCount(0);

  const folder = `ui-ufolder-${fixture.runId}`;
  await h.withConfirm(() => page.click("#mkdirBtn"), folder);
  await expect(page.locator("#view tbody tr", { hasText: folder })).toBeVisible({ timeout: 20000 });

  await page.locator(`button[data-open$="${folder}"]`).click();
  await expect(page.locator(".netdisk-crumbs")).toContainText(folder);
  await page.click("#upDir");

  await page.check("#selectAllFiles");
  for (const id of ["#batchDelete", "#batchCopy", "#batchMove", "#batchShare", "#batchDownload"]) {
    await expect(page.locator(id)).toBeEnabled();
  }
  await page.click("#batchShare");
  await expect(page.locator(".modal-backdrop.netdisk-share")).toBeVisible({ timeout: 20000 });
  await closeModals(page);
  await page.uncheck("#selectAllFiles");

  await h.withConfirm(async () => {
    await page.locator(`button[data-del$="${folder}"]`).click();
    await expect(page.locator("#view tbody tr", { hasText: folder })).toHaveCount(0, { timeout: 20000 });
  });

  h.assertClean("the user netdisk page");
});

test("mcp: a user can mint and revoke a token for their own container", async ({ page }) => {
  test.setTimeout(120000);
  test.skip(!fixture.hasUserContainer, "Docker did not provide a container for the user account");
  const h = installPage(page);
  await login(page, account.username, account.password);
  await openTab(page, "mcp");

  // The container picker only offers their own container.
  const options = await page.$$eval("#mcpCreateForm [name='containerId'] option", (els) =>
    els.map((e) => e.textContent.trim()).filter((t) => t && !t.startsWith("Select")));
  expect(options).toHaveLength(1);
  expect(options[0]).toContain(`e2e-user-${fixture.runId}`);

  const label = `ui-utoken-${fixture.runId}`;
  await page.selectOption("#mcpCreateForm [name='containerId']", { index: 1 });
  await page.fill("#mcpCreateForm [name='label']", label);
  await page.click("#mcpCreateBtn");
  await expect(page.locator(".modal-backdrop.mcp-modal")).toBeVisible({ timeout: 20000 });
  await closeModals(page);

  const row = page.locator("#view tbody tr", { hasText: label });
  await expect(row).toBeVisible();
  // No Owner column for non-admins.
  await expect(page.locator("#view thead th", { hasText: "Owner" })).toHaveCount(0);

  await h.withConfirm(async () => {
    await row.locator('button[title="Delete"]').click();
    await expect(page.locator("#view tbody tr", { hasText: label })).toHaveCount(0, { timeout: 20000 });
  });

  h.assertClean("the user MCP page");
});

test("settings: a user gets the language panel only", async ({ page }) => {
  const h = installPage(page);
  await login(page, account.username, account.password);
  await openTab(page, "scripts");

  await expect(page.locator("#userLanguageForm")).toBeVisible();
  await expect(page.locator("#adminLanguageForm"), "system default language is admin-only").toHaveCount(0);
  await expect(page.locator("#feishuForm"), "SSO config is admin-only").toHaveCount(0);
  await expect(page.locator("#newRegistryBtn"), "registries are admin-only").toHaveCount(0);

  h.assertClean("the user settings page");
});

test("usage and help show the user's own view", async ({ page }) => {
  const h = installPage(page);
  await login(page, account.username, account.password);

  await openTab(page, "usage");
  // A user's usage table holds exactly their own row.
  await expect(page.locator("#view table.data tbody tr")).toHaveCount(1);

  await openTab(page, "help");
  await expect(page.locator("#view", { hasText: "Need Admin Help?" })).toBeVisible();

  h.assertClean("the user usage/help pages");
});

test("the admin API stays closed to a user session", async ({ page }) => {
  const h = installPage(page);
  await login(page, account.username, account.password);

  // The browser session is authenticated, so a 403 here proves role checks —
  // not just the absence of a cookie.
  for (const url of [
    "/api/users",
    "/api/admin/audit?limit=10",
    "/api/admin/disks",
    "/api/admin/usage",
    "/api/registries",
    "/api/settings/feishu",
    "/api/admin/netdisk/shares",
    "/api/admin/processes",
    "/api/backup/schedule",
    "/metrics",
  ]) {
    const res = await page.request.get(url);
    expect(res.status(), `${url} must be refused for a plain user`).toBe(403);
  }

  h.assertClean("the admin API probes");
});

test("every button on every user tab is clickable without errors", async ({ page }) => {
  test.setTimeout(300000);
  const h = installPage(page);
  await login(page, account.username, account.password);

  const coverage = {};
  for (const tab of USER_TABS) {
    await openTab(page, tab);
    coverage[tab] = await clickEveryButton(page, { scope: "#view", skip: CRAWLER_SKIP_BUTTONS });
    await closeModals(page);
    h.assertClean(`clicking through the ${tab} tab`);
  }
  expect(coverage.containers.length, "buttons clicked on containers").toBeGreaterThan(0);
  expect(coverage.netdisk.length, "buttons clicked on netdisk").toBeGreaterThan(0);
  expect(coverage.volumes.length, "buttons clicked on volumes").toBeGreaterThan(0);
});

test("a user can log out", async ({ page }) => {
  const h = installPage(page);
  await login(page, account.username, account.password);
  await logout(page);
  await page.reload();
  await expect(page.locator("#loginForm")).toBeVisible();
  h.assertClean("user logout");
});
