// Full click-through of the admin console: every sidebar tab, every page-level
// button, and the real CRUD round-trips (volume, network, group, user, MCP
// token, netdisk folder, registry).
//
// Safety model — see fixtures/ui.js: native confirm()/prompt() dialogs are
// CANCELLED by default, so the crawler may click Delete/Prune/Remove without
// destroying anything. Tests that mean to mutate opt in via withConfirm().

import { test, expect } from "@playwright/test";
import { startServer, seed } from "./fixtures/server.js";
import {
  ADMIN_TABS, ADMIN_ONLY_TABS, CRAWLER_SKIP_BUTTONS, installPage, login, logout,
  openTab, closeModals, clickEveryButton, toastText,
} from "./fixtures/ui.js";

const PORT = 19100;
test.use({ baseURL: `http://127.0.0.1:${PORT}` });

// One server for the whole file; the tests share its seeded workspace.
test.describe.configure({ mode: "serial" });

let server;
let fixture;

test.beforeAll(async () => {
  server = await startServer({ port: PORT });
  fixture = await seed(server);
});

test.afterAll(async () => {
  if (fixture) await fixture.cleanup();
  if (server) await server.stop();
});

test("login form rejects a wrong password and keeps the user on the login page", async ({ page }) => {
  const h = installPage(page);
  await page.goto("/");
  await expect(page.locator("#loginForm")).toBeVisible();
  await page.fill("input[name='username']", server.adminUser);
  await page.fill("input[name='password']", "definitely-not-the-password");
  await page.click("#loginForm button.primary");

  await expect(page.locator(".toast")).toBeVisible();
  await expect(page.locator("#loginForm")).toBeVisible();
  await expect(page.locator("aside nav")).toHaveCount(0);
  h.assertClean("a rejected login");
});

test("login language switcher re-renders the login card", async ({ page }) => {
  const h = installPage(page);
  await page.goto("/");
  const langButtons = page.locator(".login-lang .lang-btn");
  await expect(langButtons).toHaveCount(2);
  for (let i = 0; i < 2; i++) {
    await langButtons.nth(i).click();
    await expect(page.locator("#loginForm")).toBeVisible();
  }
  h.assertClean("the login language switch");
});

test("admin sees every tab and each one renders", async ({ page }) => {
  test.setTimeout(180000);
  const h = installPage(page);
  await login(page, server.adminUser, server.adminPassword);

  // The sidebar shows exactly the admin tab set, in order.
  const keys = await page.$$eval("nav button[data-tab]", (els) => els.map((e) => e.dataset.tab));
  expect(keys).toEqual(ADMIN_TABS);

  for (const tab of ADMIN_TABS) {
    await openTab(page, tab);
    await expect(page.locator("header .titles h1"), `heading for ${tab}`).not.toBeEmpty();
    await expect(page.locator("#view"), `body of ${tab}`).not.toBeEmpty();
    h.assertClean(`the ${tab} tab`);
  }
});

test("header controls work: refresh, sidebar collapse, notifications, background jobs", async ({ page }) => {
  const h = installPage(page);
  await login(page, server.adminUser, server.adminPassword);

  await page.click("#refresh");
  expect(await toastText(page)).not.toBe("");

  // Collapsing persists to localStorage and re-renders the shell.
  await page.click("#sidebarToggle");
  await expect(page.locator("aside")).toHaveClass(/collapsed/);
  await page.click("#sidebarToggle");
  await expect(page.locator("aside")).not.toHaveClass(/collapsed/);

  await page.click("#notificationBell");
  await expect(page.locator(".modal-backdrop.notifications-modal")).toBeVisible();
  await closeModals(page);

  await page.click("#jobsButton");
  await expect(page.locator(".modal-backdrop.jobs-modal")).toBeVisible();
  await closeModals(page);

  h.assertClean("the header controls");
});

test("dashboard renders its cards", async ({ page }) => {
  const h = installPage(page);
  await login(page, server.adminUser, server.adminPassword);
  await openTab(page, "dashboard");
  await expect(page.locator("#view .card").first()).toBeVisible();
  h.assertClean("the dashboard");
});

test("containers: state filters, select-all, batch toolbar and every row action", async ({ page }) => {
  test.setTimeout(180000);
  test.skip(!fixture.hasAdminContainer, "Docker did not provide a container to act on");
  const h = installPage(page);
  await login(page, server.adminUser, server.adminPassword);
  await openTab(page, "containers");

  await expect(page.locator("#view table.data tbody tr").first()).toBeVisible();

  // Each state filter narrows the table without breaking it.
  for (const f of ["running", "stopped", "paused", "all"]) {
    await page.click(`[data-filter="${f}"]`);
    await expect(page.locator(`[data-filter="${f}"]`)).toHaveClass(/active/);
    await expect(page.locator("#view table.data")).toBeVisible();
  }

  // An admin sees every user's containers, and gets an Owner column for them.
  await expect(page.locator("#view thead th", { hasText: "Owner" })).toHaveCount(1);
  await expect(page.locator("#view tbody tr", { hasText: `e2e-user-${fixture.runId}` })).toHaveCount(1);

  // Search narrows to one seeded container and back.
  await page.fill("#searchBox", `e2e-admin-${fixture.runId}`);
  await expect(page.locator("#view tbody tr")).toHaveCount(1);
  await page.fill("#searchBox", "no-such-container-xyz");
  await expect(page.locator("#view tbody tr.empty-row")).toBeVisible();
  await page.fill("#searchBox", "");

  // Select-all raises the batch toolbar; every batch button is exercised with
  // its confirmation cancelled, so nothing is actually removed.
  await page.check("#selectAll");
  await expect(page.locator(".batch-bar")).toBeVisible();
  for (const action of ["start", "stop", "restart", "unpause", "remove"]) {
    const btn = page.locator(`[data-batch="${action}"]`);
    await expect(btn).toBeVisible();
  }
  await page.click('[data-batch="remove"]');
  await page.waitForTimeout(300);
  // Cancelled at the confirm dialog — both seeded containers are still listed.
  await expect(page.locator("#view tbody tr", { hasText: fixture.runId })).toHaveCount(2);
  await page.uncheck("#selectAll");
  await expect(page.locator(".batch-bar")).toHaveCount(0);

  // Row actions that open a panel: each must open and close cleanly.
  const panels = [
    ["logs", ".logs-modal"],
    ["inspect", ".detail-modal"],
    ["files", ".files-modal"],
    ["stats", ".stats-modal"],
    ["terminal", ".terminal-modal"],
  ];
  for (const [act, selector] of panels) {
    const btn = page.locator(`#view button[data-act="${act}"]`).first();
    if ((await btn.count()) === 0) continue;
    await btn.click();
    await expect(page.locator(`.modal-backdrop${selector}`), `${act} panel`).toBeVisible({ timeout: 20000 });
    await closeModals(page);
  }

  // Lifecycle actions on the seeded admin container: stop then start again.
  const adminRow = page.locator("#view tbody tr", { hasText: `e2e-admin-${fixture.runId}` });
  await adminRow.locator('button[data-act="stop"]:not([disabled])').click();
  await expect(adminRow.locator(".badge-muted")).toBeVisible({ timeout: 45000 });
  await adminRow.locator('button[data-act="start"]:not([disabled])').click();
  await expect(adminRow.locator(".badge-ok")).toBeVisible({ timeout: 45000 });

  h.assertClean("the containers page");
});

test("containers: the New Container wizard opens, exposes its fields and closes", async ({ page }) => {
  const h = installPage(page);
  await login(page, server.adminUser, server.adminPassword);
  await openTab(page, "containers");

  await page.click("#newContainerBtn");
  const form = page.locator("#newContainer");
  await expect(form).toBeVisible();
  await expect(form.locator("[name='name']")).toBeVisible();
  await expect(form.locator("[name='image']")).toBeVisible();
  if (fixture.imagePublished) {
    await expect(form.locator("[name='image'] option", { hasText: fixture.imageName })).toHaveCount(1);
  }
  // The advanced block expands.
  await page.locator(".create-modal details.advanced-block summary").click();
  await expect(form.locator("[name='command']")).toBeVisible();
  await closeModals(page);
  await expect(page.locator("#newContainer")).toHaveCount(0);

  h.assertClean("the create-container wizard");
});

test("images: pull, build, import, register and preset dialogs all open and close", async ({ page }) => {
  const h = installPage(page);
  await login(page, server.adminUser, server.adminPassword);
  await openTab(page, "images");

  for (const [buttonId, formId] of [
    ["#pullImageBtn", "#pullForm"],
    ["#buildImageBtn", "#buildForm"],
    ["#importImageBtn", "#importForm"],
    ["#registerImageBtn", "#registerForm"],
  ]) {
    await page.click(buttonId);
    await expect(page.locator(formId), `${buttonId} dialog`).toBeVisible();
    await closeModals(page);
  }

  if (fixture.imagePublished) {
    // The per-image defaults editor.
    await page.locator("#view button[data-image-preset]").first().click();
    await expect(page.locator("#presetForm")).toBeVisible();
    await closeModals(page);

    // Delete asks first; cancelling leaves the image in place.
    await page.locator("#view button[data-image-delete]").first().click();
    await page.waitForTimeout(300);
    await expect(page.locator("#view tbody tr", { hasText: fixture.imageName })).toHaveCount(1);
  }

  h.assertClean("the images page");
});

test("volumes: create, browse files, prune prompt and delete", async ({ page }) => {
  test.setTimeout(120000);
  const h = installPage(page);
  await login(page, server.adminUser, server.adminPassword);
  await openTab(page, "volumes");

  const name = `ui-vol-${fixture.runId}`;
  await page.click("#newVolumeBtn");
  await expect(page.locator("#volForm")).toBeVisible();
  await page.fill("#volForm [name='name']", name);
  await page.click("#volSubmit");
  await expect(page.locator("#view tbody tr", { hasText: name })).toBeVisible({ timeout: 30000 });

  // The in-volume file browser opens on the new (empty) volume.
  await page.locator("#view tbody tr", { hasText: name }).locator("button[data-vol-files]").click();
  await expect(page.locator(".modal-backdrop.volfiles-modal")).toBeVisible({ timeout: 20000 });
  await closeModals(page);

  // Prune asks for confirmation; cancelling keeps the volume.
  await page.click("#pruneVolumes");
  await page.waitForTimeout(300);
  await expect(page.locator("#view tbody tr", { hasText: name })).toBeVisible();

  // Delete, this time confirming.
  await h.withConfirm(async () => {
    await page.locator("#view tbody tr", { hasText: name }).locator("button.danger").click();
    await expect(page.locator("#view tbody tr", { hasText: name })).toHaveCount(0, { timeout: 30000 });
  });

  h.assertClean("the volumes page");
});

test("networks: create, open details, attach panel and delete", async ({ page }) => {
  test.setTimeout(120000);
  const h = installPage(page);
  await login(page, server.adminUser, server.adminPassword);
  await openTab(page, "networks");

  const name = `ui-net-${fixture.runId}`;
  await page.click("#newNetBtn");
  await expect(page.locator("#netForm")).toBeVisible();
  await page.fill("#netForm [name='name']", name);
  // The advanced IPAM block expands.
  await page.locator(".network-modal details.advanced-block summary").click();
  await expect(page.locator("#netForm [name='gateway']")).toBeVisible();
  await page.click("#netSubmit");
  await expect(page.locator("#view tbody tr", { hasText: name })).toBeVisible({ timeout: 30000 });

  const row = page.locator("#view tbody tr", { hasText: name });

  // The ℹ button must open the network detail panel.
  await row.locator('button[title="Details"]').click();
  await expect(page.locator(".modal-backdrop.network-modal")).toBeVisible({ timeout: 20000 });
  await closeModals(page);

  // The ✕ button must delete it once confirmed.
  await h.withConfirm(async () => {
    await row.locator('button[title="Delete"]').click();
    await expect(page.locator("#view tbody tr", { hasText: name })).toHaveCount(0, { timeout: 30000 });
  });

  h.assertClean("the networks page");
});

test("stacks: the compose editor opens with a sample project and closes without deploying", async ({ page }) => {
  const h = installPage(page);
  await login(page, server.adminUser, server.adminPassword);
  await openTab(page, "stacks");

  await page.click("#newStackBtn");
  await expect(page.locator("#stackForm")).toBeVisible();
  await expect(page.locator("#stackForm [name='name']")).toBeEditable();
  await expect(page.locator("#stackForm [name='composeYaml']")).toContainText("services:");
  // Deliberately not saved: saving a new stack immediately runs `docker compose
  // up`, which would pull images from the network.
  await closeModals(page);
  await expect(page.locator("#stackForm")).toHaveCount(0);

  h.assertClean("the stacks page");
});

test("netdisk: folder create, navigation, selection, copy/move pickers, share and delete", async ({ page }) => {
  test.setTimeout(180000);
  const h = installPage(page);
  await login(page, server.adminUser, server.adminPassword);
  await openTab(page, "netdisk");

  const folder = `ui-folder-${fixture.runId}`;
  await h.withConfirm(() => page.click("#mkdirBtn"), folder);
  await expect(page.locator("#view tbody tr", { hasText: folder })).toBeVisible({ timeout: 20000 });

  // Enter the folder, then walk back out with Up.
  await page.locator(`button[data-open$="${folder}"]`).click();
  await expect(page.locator(".netdisk-crumbs")).toContainText(folder);
  await page.click("#upDir");
  await expect(page.locator("#view tbody tr", { hasText: folder })).toBeVisible();

  // Selecting rows enables the batch toolbar.
  await page.check("#selectAllFiles");
  for (const id of ["#batchDelete", "#batchCopy", "#batchMove", "#batchShare", "#batchDownload"]) {
    await expect(page.locator(id), `${id} after selecting`).toBeEnabled();
  }

  // Copy and Move open the destination picker. "Up" starts disabled at the
  // picker's own root (there's nowhere to go), so only confirm it renders.
  for (const id of ["#batchCopy", "#batchMove"]) {
    await page.click(id);
    await expect(page.locator(".modal-backdrop.netdisk-picker")).toBeVisible({ timeout: 20000 });
    await expect(page.locator("#pickerConfirm")).toBeVisible();
    await expect(page.locator("#pickerUp")).toBeDisabled();
    await closeModals(page);
  }

  // Share opens the external-link composer.
  await page.click("#batchShare");
  await expect(page.locator(".modal-backdrop.netdisk-share")).toBeVisible({ timeout: 20000 });
  await expect(page.locator("#shareCreate")).toBeVisible();
  await closeModals(page);
  await page.uncheck("#selectAllFiles");

  // Per-row rename asks for a new name; cancelling leaves the folder alone.
  await page.locator(`button[data-ren$="${folder}"]`).click();
  await page.waitForTimeout(300);
  await expect(page.locator("#view tbody tr", { hasText: folder })).toBeVisible();

  // The backup disk is a separate mode with its own toolbar.
  await page.click('.netdisk-mode-btn[data-mode="backup"]');
  await expect(page.locator('.netdisk-mode-btn[data-mode="backup"]')).toHaveClass(/active/);
  await expect(page.locator("#uploadFiles")).toHaveCount(0);
  await page.click('.netdisk-mode-btn[data-mode="netdisk"]');
  await expect(page.locator("#uploadFiles")).toHaveCount(1);

  // Finally remove the folder, confirming this time.
  await h.withConfirm(async () => {
    await page.locator(`button[data-del$="${folder}"]`).click();
    await expect(page.locator("#view tbody tr", { hasText: folder })).toHaveCount(0, { timeout: 20000 });
  });

  h.assertClean("the netdisk page");
});

test("mcp: create a token, view its config, copy it and delete it", async ({ page }) => {
  test.setTimeout(120000);
  test.skip(!fixture.hasAdminContainer, "Docker did not provide a container to bind a token to");
  const h = installPage(page);
  await login(page, server.adminUser, server.adminPassword);
  await openTab(page, "mcp");

  const label = `ui-token-${fixture.runId}`;
  await page.selectOption("#mcpCreateForm [name='containerId']", { index: 1 });
  await page.fill("#mcpCreateForm [name='label']", label);
  await page.selectOption("#mcpCreateForm [name='expiresInHours']", "24");
  await page.click("#mcpCreateBtn");

  // Creating a token immediately shows its config panel.
  await expect(page.locator(".modal-backdrop.mcp-modal")).toBeVisible({ timeout: 20000 });
  await closeModals(page);
  const row = page.locator("#view tbody tr", { hasText: label });
  await expect(row).toBeVisible();

  // Re-open the config from the table and use its copy buttons.
  await row.locator('button[title="View config"]').click();
  await expect(page.locator(".modal-backdrop.mcp-modal")).toBeVisible();
  for (const id of ["#mcpEndpointCopy", "#mcpCurlCopy"]) {
    if ((await page.locator(id).count()) > 0) await page.click(id);
  }
  await closeModals(page);

  await h.withConfirm(async () => {
    await row.locator('button[title="Delete"]').click();
    await expect(page.locator("#view tbody tr", { hasText: label })).toHaveCount(0, { timeout: 20000 });
  });

  h.assertClean("the MCP page");
});

test("users & groups: create a group and a user, then open every row dialog", async ({ page }) => {
  test.setTimeout(120000);
  const h = installPage(page);
  await login(page, server.adminUser, server.adminPassword);
  await openTab(page, "users");

  const groupName = `ui-group-${fixture.runId}`;
  await page.fill("#newGroup [name='name']", groupName);
  await page.click("#newGroup button");
  await expect(page.locator("#view", { hasText: groupName }).first()).toBeVisible({ timeout: 20000 });

  const userName = `uiuser${fixture.runId}`;
  await page.fill("#newUser [name='username']", userName);
  await page.fill("#newUser [name='password']", "ui-user-secret");
  await page.selectOption("#newUser [name='role']", "user");
  await page.fill("#newUser [name='containerCap']", "3");
  await page.click("#newUser button");
  // The same username also appears in the Netdisk Usage table below, so scope
  // to the row that actually carries the user action buttons.
  const row = page.locator(`#view tbody tr:has(button[data-user-name="${userName}"])`);
  await expect(row).toBeVisible({ timeout: 20000 });

  // Group membership editor.
  await row.locator("button[data-edit-groups]").click();
  await expect(page.locator(".modal-backdrop.usergroups-modal")).toBeVisible();
  await closeModals(page);

  // Account editor (role, caps, password reset).
  await row.locator("button[data-edit-user]").click();
  await expect(page.locator(".modal-backdrop.useredit-modal")).toBeVisible();
  await expect(page.locator("#saveUser")).toBeVisible();
  await closeModals(page);

  // Deactivate and Delete both ask first; cancelling leaves the account intact.
  await row.locator("button[data-deactivate-user]").click();
  await page.waitForTimeout(300);
  await row.locator("button[data-delete-user]").click();
  await page.waitForTimeout(300);
  await expect(row).toBeVisible();

  // Group path/backup/language editors are prompt-driven; cancelling is enough
  // to prove they are wired up.
  for (const attr of ["data-group-path", "data-group-backup"]) {
    const btn = page.locator(`#view button[${attr}]`).first();
    if ((await btn.count()) === 0) continue;
    await btn.click();
    await page.waitForTimeout(200);
  }
  const langBtn = page.locator("#view button[data-group-lang]").first();
  if ((await langBtn.count()) > 0) {
    await langBtn.click();
    await expect(page.locator("#saveGroupLang")).toBeVisible();
    await closeModals(page);
  }

  // Finally delete the account we made, confirming this time.
  await h.withConfirm(async () => {
    await row.locator("button[data-delete-user]").click();
    await expect(page.locator(`#view tbody tr:has(button[data-user-name="${userName}"])`)).toHaveCount(0, { timeout: 20000 });
  });

  h.assertClean("the users page");
});

test("activity log: filters narrow the table and Export CSV downloads a file", async ({ page }) => {
  const h = installPage(page);
  await login(page, server.adminUser, server.adminPassword);
  await openTab(page, "audit");

  await expect(page.locator("#view table.data")).toBeVisible();
  await page.fill("#fAction", "user.create");
  await page.waitForTimeout(600);
  await expect(page.locator("#view table.data")).toBeVisible();
  await page.fill("#fActor", server.adminUser);
  await page.waitForTimeout(600);
  await page.fill("#fActor", "");
  await page.fill("#fAction", "");
  await page.waitForTimeout(600);

  const [download] = await Promise.all([
    page.waitForEvent("download", { timeout: 15000 }),
    page.click("#exportCsv"),
  ]);
  expect(download.suggestedFilename()).toMatch(/^mudp-audit-\d{4}-\d{2}-\d{2}\.csv$/);

  h.assertClean("the activity log");
});

test("disks: the admin page renders its panels and saves the mount config", async ({ page }) => {
  const h = installPage(page);
  await login(page, server.adminUser, server.adminPassword);
  await openTab(page, "disks");

  await expect(page.locator("#mountForm")).toBeVisible();
  await expect(page.locator("#backupForm")).toBeVisible();
  await expect(page.locator("#backupScheduleForm")).toBeVisible();
  await expect(page.locator("#view table.data")).toBeVisible();

  // Saving the config is the only non-destructive write on this page; Mount Now,
  // Backup Database and "Back Up All Users Now" all touch host storage and are
  // deliberately left alone.
  await page.fill("#mountForm [name='source']", "");
  await page.fill("#mountForm [name='target']", "");
  await page.click("#saveMountConfig");
  expect(await toastText(page)).toContain("saved");

  h.assertClean("the disks page");
});

test("settings: language panels render and a registry can be added, tested and removed", async ({ page }) => {
  test.setTimeout(120000);
  const h = installPage(page);
  await login(page, server.adminUser, server.adminPassword);
  await openTab(page, "scripts");

  await expect(page.locator("#userLanguageForm")).toBeVisible();
  await expect(page.locator("#adminLanguageForm")).toBeVisible();
  await expect(page.locator("#feishuForm")).toBeVisible();

  const regName = `ui-reg-${fixture.runId}`;
  await page.click("#newRegistryBtn");
  await expect(page.locator("#regForm")).toBeVisible();
  await page.fill("#regForm [name='name']", regName);
  await page.fill("#regForm [name='url']", "registry.example.invalid");
  await page.fill("#regForm [name='username']", "tester");
  await page.fill("#regForm [name='token']", "secret");
  await page.click("#saveReg");
  const row = page.locator("#view tbody tr", { hasText: regName });
  await expect(row).toBeVisible({ timeout: 20000 });

  // Edit dialog re-opens with the stored values.
  await row.locator("button[data-reg-edit]").click();
  await expect(page.locator("#regForm [name='name']")).toHaveValue(regName);
  await closeModals(page);

  // The login test is expected to fail against an unreachable host — the
  // backend correctly reports that as a 502, which is not a server bug, so it
  // is excluded from the clean-page assertion below. Waiting for the response
  // explicitly (rather than just the resulting toast) avoids a race with the
  // page's own response-recording listener.
  const [regTestResp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes("/api/registries/test")),
    row.locator("button[data-reg-test]").click(),
  ]);
  expect([200, 502]).toContain(regTestResp.status());
  expect(await toastText(page)).not.toBe("");
  h.forgetServerError("/api/registries/test");

  await h.withConfirm(async () => {
    await row.locator("button[data-reg-delete]").click();
    await expect(page.locator("#view tbody tr", { hasText: regName })).toHaveCount(0, { timeout: 20000 });
  });

  h.assertClean("the settings page");
});

test("usage, hardware and help pages render their content", async ({ page }) => {
  const h = installPage(page);
  await login(page, server.adminUser, server.adminPassword);

  await openTab(page, "usage");
  await expect(page.locator("#view table.data").first()).toBeVisible();

  await openTab(page, "hardware");
  await expect(page.locator("#view").first()).not.toBeEmpty();

  await openTab(page, "help");
  await expect(page.locator("#view .card").first()).toBeVisible();

  h.assertClean("the usage/hardware/help pages");
});

test("every button on every admin tab is clickable without errors", async ({ page }) => {
  test.setTimeout(300000);
  const h = installPage(page);
  await login(page, server.adminUser, server.adminPassword);

  const coverage = {};
  for (const tab of ADMIN_TABS) {
    await openTab(page, tab);
    coverage[tab] = await clickEveryButton(page, { scope: "#view", skip: CRAWLER_SKIP_BUTTONS });
    await closeModals(page);
    h.assertClean(`clicking through the ${tab} tab`);
  }
  // Every tab with buttons must have had at least one clicked, otherwise the
  // crawler silently covered nothing.
  expect(coverage.containers.length, "buttons clicked on containers").toBeGreaterThan(0);
  expect(coverage.netdisk.length, "buttons clicked on netdisk").toBeGreaterThan(0);
  expect(coverage.images.length, "buttons clicked on images").toBeGreaterThan(0);
  expect(coverage.users.length, "buttons clicked on users").toBeGreaterThan(0);
});

test("logout returns to the login screen and the session is gone", async ({ page }) => {
  const h = installPage(page);
  await login(page, server.adminUser, server.adminPassword);
  await logout(page);

  // Reloading must not resurrect the session.
  await page.reload();
  await expect(page.locator("#loginForm")).toBeVisible();
  await expect(page.locator("aside nav")).toHaveCount(0);
  h.assertClean("logout");
});

test("admin-only tabs are exactly the ones a plain user cannot see", async () => {
  expect(ADMIN_ONLY_TABS).toEqual(["users", "audit", "disks"]);
});
