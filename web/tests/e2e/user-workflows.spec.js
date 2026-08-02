// Complex, multi-step user operation flows that the other specs don't cover:
// each test here chains several real actions together (across tabs, across
// two logged-in sessions, or across a cancel/retry) and asserts on the state
// that survives the whole sequence, rather than on one isolated click.
//
// Same safety model as the rest of the suite — see fixtures/ui.js: native
// confirm()/prompt() dialogs are cancelled unless a test opts in via
// withConfirm().

import { test, expect } from "@playwright/test";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { startServer, seed } from "./fixtures/server.js";
import { installPage, login, openTab, closeModals, toastText } from "./fixtures/ui.js";

const PORT = 19104;
test.use({ baseURL: `http://127.0.0.1:${PORT}` });

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

test("double-submitting the new-volume form leaves exactly one volume behind", async ({ page }) => {
  test.setTimeout(60000);
  const h = installPage(page);
  await login(page, server.adminUser, server.adminPassword);
  await openTab(page, "volumes");

  const name = `ui-dbl-${fixture.runId}`;
  await page.click("#newVolumeBtn");
  await page.fill("#volForm [name='name']", name);
  // Fire both submissions before either response comes back, the way a
  // double-click or an impatient user mashing the button really would —
  // clicking the locator twice in sequence would just wait for the first
  // click's re-render, which defeats the point of the test.
  await page.evaluate(() => {
    const btn = document.querySelector("#volSubmit");
    btn.click();
    btn.click();
  });

  const rows = page.locator("#view tbody tr", { hasText: name });
  await expect(rows).toHaveCount(1, { timeout: 30000 });
  // Give any second, slower response a moment to land before declaring victory.
  await page.waitForTimeout(1000);
  await expect(rows).toHaveCount(1);

  h.assertClean("the double-submitted volume creation");
});

test("a container created through the wizard can mount an existing volume, and the real mount shows up in its inspect panel", async ({ page }) => {
  test.setTimeout(120000);
  test.skip(!fixture.imagePublished, "no image was published for the container wizard to use");
  const h = installPage(page);
  await login(page, server.adminUser, server.adminPassword);

  // 1. Create the volume the container is about to depend on.
  await openTab(page, "volumes");
  const volName = `ui-mountvol-${fixture.runId}`;
  await page.click("#newVolumeBtn");
  await page.fill("#volForm [name='name']", volName);
  await page.click("#volSubmit");
  await expect(page.locator("#view tbody tr", { hasText: volName })).toBeVisible({ timeout: 30000 });

  // 2. Create a container that mounts it by name — the same cross-reference a
  // real user types after reading the "Available volumes" hint in the form.
  await openTab(page, "containers");
  const containerName = `ui-wf-mount-${fixture.runId}`;
  await page.click("#newContainerBtn");
  await page.fill("#newContainer [name='name']", containerName);
  await page.selectOption("#newContainer [name='image']", fixture.imageName);
  await page.fill("#newContainer [name='mounts']", `${volName}:/data`);
  await page.click("#createSubmit");

  // The wizard streams progress and closes itself ~700ms after the "done"
  // event; wait for that close rather than a fixed sleep.
  await expect(page.locator("#newContainer")).toHaveCount(0, { timeout: 60000 });
  const row = page.locator("#view tbody tr", { hasText: containerName });
  await expect(row).toBeVisible({ timeout: 30000 });

  // 3. The inspect panel is the one place that reflects the real Docker mount,
  // not just what the create form remembered submitting.
  await row.locator('button[data-act="inspect"]').click();
  await expect(page.locator(".modal-backdrop.detail-modal")).toBeVisible({ timeout: 20000 });
  await expect(page.locator(".detail")).toContainText(volName);
  await expect(page.locator(".detail")).toContainText("/data");
  await closeModals(page);

  h.assertClean("the volume-mounting container workflow");
});

test("netdisk: nested folders, a breadcrumb jump and a move-up-a-level all keep the tree consistent", async ({ page }) => {
  test.setTimeout(120000);
  const h = installPage(page);
  await login(page, server.adminUser, server.adminPassword);
  await openTab(page, "netdisk");

  const folderA = `ui-wf-a-${fixture.runId}`;
  const folderB = `ui-wf-b-${fixture.runId}`;
  const fileName = `ui-wf-file-${fixture.runId}.txt`;

  // Build A/B and drop a file at the bottom of it.
  await h.withConfirm(() => page.click("#mkdirBtn"), folderA);
  await expect(page.locator("#view tbody tr", { hasText: folderA })).toBeVisible({ timeout: 20000 });
  await page.locator(`button[data-open$="${folderA}"]`).click();
  await expect(page.locator(".netdisk-crumbs")).toContainText(folderA);

  await h.withConfirm(() => page.click("#mkdirBtn"), folderB);
  await expect(page.locator("#view tbody tr", { hasText: folderB })).toBeVisible({ timeout: 20000 });
  await page.locator(`button[data-open$="${folderB}"]`).click();
  await expect(page.locator(".netdisk-crumbs")).toContainText(folderB);

  const tmpPath = path.join(os.tmpdir(), fileName);
  fs.writeFileSync(tmpPath, `nested workflow file — ${fixture.runId}\n`);
  try {
    await page.setInputFiles("#uploadFiles", tmpPath);
    const fileRow = page.locator("table.netdisk-table tbody tr", { hasText: fileName });
    await expect(fileRow).toBeVisible({ timeout: 20000 });

    // Move the file up into A via the destination picker, without leaving B.
    await fileRow.locator(".netdisk-row-check").check();
    await page.click("#batchMove");
    await expect(page.locator(".modal-backdrop.netdisk-picker")).toBeVisible({ timeout: 20000 });
    // Sitting at the file's own parent folder, the picker refuses to "move" it
    // nowhere: Confirm starts disabled until a different destination is picked.
    await expect(page.locator("#pickerConfirm")).toBeDisabled();
    await page.click("#pickerUp");
    await expect(page.locator("#pickerConfirm")).toBeEnabled();
    await page.click("#pickerConfirm");

    // Still inside B: the file must be gone from here...
    await expect(page.locator("table.netdisk-table tbody tr", { hasText: fileName })).toHaveCount(0, { timeout: 20000 });
    // ...and present one level up, in A.
    await page.click("#upDir");
    await expect(page.locator("#view tbody tr", { hasText: fileName })).toBeVisible({ timeout: 20000 });

    // A breadcrumb jump straight to the root must skip both levels at once,
    // not just undo the last "Up" the way a second click on Up would.
    await page.locator('.netdisk-crumbs button[data-crumb=""]').click();
    await expect(page.locator(".netdisk-crumbs")).not.toContainText(folderA);
    await expect(page.locator("#view tbody tr", { hasText: folderA })).toBeVisible({ timeout: 20000 });

    await h.withConfirm(async () => {
      await page.locator(`button[data-del$="${folderA}"]`).click();
      await expect(page.locator("#view tbody tr", { hasText: folderA })).toHaveCount(0, { timeout: 20000 });
    });
  } finally {
    fs.rmSync(tmpPath, { force: true });
  }

  h.assertClean("the nested-folder move workflow");
});

test("cancelling the user-edit dialog discards the edit; reopening shows the saved value, not the draft", async ({ page }) => {
  const h = installPage(page);
  await login(page, server.adminUser, server.adminPassword);
  await openTab(page, "users");

  const row = page.locator(`#view tbody tr:has(button[data-user-name="${fixture.user.username}"])`);
  const capField = "#editUser [name='containerCap']";

  await row.locator("button[data-edit-user]").click();
  await expect(page.locator(".modal-backdrop.useredit-modal")).toBeVisible();
  const originalCap = await page.locator(capField).inputValue();

  // Change the value but back out via Cancel instead of Save.
  await page.fill(capField, "987");
  await closeModals(page);

  // Reopening must show the account's real, persisted value — not the 987
  // that was typed and abandoned in the last dialog instance.
  await row.locator("button[data-edit-user]").click();
  await expect(page.locator(".modal-backdrop.useredit-modal")).toBeVisible();
  await expect(page.locator(capField)).toHaveValue(originalCap);
  await closeModals(page);

  h.assertClean("the cancelled user-edit dialog");
});

test("lowering a user's container cap while they are logged in blocks their very next create — no re-login required", async ({ page, browser }) => {
  test.setTimeout(90000);
  test.skip(!fixture.hasUserContainer, "Docker did not provide a running container for the user account to be at-capacity with");
  const h = installPage(page);

  // The user logs in and never reloads or logs out again in this test — the
  // point is that the server, not a stale session snapshot, enforces the cap.
  await login(page, fixture.user.username, fixture.user.password);
  await openTab(page, "containers");

  const adminCtx = await browser.newContext();
  const adminPage = await adminCtx.newPage();
  try {
    await login(adminPage, server.adminUser, server.adminPassword);
    await openTab(adminPage, "users");
    const row = adminPage.locator(`#view tbody tr:has(button[data-user-name="${fixture.user.username}"])`);
    await row.locator("button[data-edit-user]").click();
    await expect(adminPage.locator(".modal-backdrop.useredit-modal")).toBeVisible();
    // 0 means "leave unchanged" server-side (store.UpdateUser's sentinel for a
    // no-op field, same convention as an empty password), so 1 is the actual
    // at-capacity value here — the seeded fixture container already puts the
    // user's running count at 1.
    await adminPage.fill("#editUser [name='containerCap']", "1");
    await adminPage.click("#saveUser");
    await expect(adminPage.locator(".modal-backdrop.useredit-modal")).toHaveCount(0, { timeout: 15000 });
  } finally {
    await adminCtx.close();
  }

  // Back on the user's untouched session: the next create attempt must be
  // rejected by the same request that would have created it, not silently
  // allowed through on cached permissions.
  await page.click("#newContainerBtn");
  await page.fill("#newContainer [name='name']", `ui-wf-capped-${fixture.runId}`);
  await page.selectOption("#newContainer [name='image']", fixture.imageName);
  await page.click("#createSubmit");

  await expect(page.locator(".error-box")).toContainText("container limit reached", { timeout: 20000 });
  expect(await toastText(page)).toContain("container limit reached");
  await closeModals(page);

  h.assertClean("the live container-cap enforcement");
});

test("removing a user's last group locks their live session out entirely — no fresh login needed", async ({ page, browser }) => {
  test.setTimeout(60000);
  const h = installPage(page);

  // The user's session stays open for the whole test: same login, same page,
  // no reload — only navigating between tabs the way a real user would.
  await login(page, fixture.user.username, fixture.user.password);
  await openTab(page, "netdisk");
  await expect(page.locator("#view .error-box")).toHaveCount(0);

  const adminCtx = await browser.newContext();
  const adminPage = await adminCtx.newPage();
  try {
    await login(adminPage, server.adminUser, server.adminPassword);
    await openTab(adminPage, "users");
    const row = adminPage.locator(`#view tbody tr:has(button[data-user-name="${fixture.user.username}"])`);
    await row.locator("button[data-edit-groups]").click();
    await expect(adminPage.locator(".modal-backdrop.usergroups-modal")).toBeVisible();

    // Strip every group membership. The real checkbox is visually hidden
    // (opacity:0, styled via its label's ::before), so click the visible
    // label the way a real user would rather than the input itself.
    const checkedLabel = adminPage.locator("#groupForm label.check:has(input[name='groupIds']:checked)");
    while ((await checkedLabel.count()) > 0) await checkedLabel.first().click();
    await adminPage.click("#saveGroups");
    await expect(adminPage.locator(".modal-backdrop.usergroups-modal")).toHaveCount(0, { timeout: 15000 });
  } finally {
    await adminCtx.close();
  }

  // Back on the user's untouched session: browsing away and back to netdisk is
  // the only "refresh" a real user would do — no logout/login in between —
  // and the very next request must already see the change. A groupless
  // account reads as "pending" server-side (authz.go), so every API call
  // starts refusing with the same 403 a brand-new, never-approved signup
  // gets — not just the netdisk-specific "no path configured" error.
  await openTab(page, "dashboard");
  await openTab(page, "netdisk");
  await expect(page.locator("#view .error-box")).toContainText("account pending approval", { timeout: 15000 });

  h.assertClean("the live group-removal enforcement");
});

// ---------------------------------------------------------------------------
// Prompt data refresh: refresh.js polls each tab on its own interval
// (containers every 5s, dashboard every 8s — see TAB_REFRESH) and every tab
// click also fires an immediate quiet refresh (refreshActiveTab). The tests
// below prove both paths actually keep what's on screen current — a second,
// untouched session or tab must show a change made elsewhere without the
// viewer ever clicking the manual Refresh button.
// ---------------------------------------------------------------------------

test("containers: a state change made in one session appears in another open session's table on its own", async ({ page, browser }) => {
  test.setTimeout(60000);
  test.skip(!fixture.hasAdminContainer, "Docker did not provide a container to act on");
  const h = installPage(page);
  await login(page, server.adminUser, server.adminPassword);
  await openTab(page, "containers");
  const row = page.locator("#view tbody tr", { hasText: `e2e-admin-${fixture.runId}` });
  await expect(row.locator(".badge-ok")).toBeVisible();

  const otherCtx = await browser.newContext();
  const otherPage = await otherCtx.newPage();
  try {
    await login(otherPage, server.adminUser, server.adminPassword);
    await openTab(otherPage, "containers");
    const otherRow = otherPage.locator("#view tbody tr", { hasText: `e2e-admin-${fixture.runId}` });
    await expect(otherRow.locator(".badge-ok")).toBeVisible();

    // Stopped from the first session only — the second session never clicks
    // anything from here on, it just sits on the containers tab.
    await row.locator('button[data-act="stop"]:not([disabled])').click();
    await expect(row.locator(".badge-muted")).toBeVisible({ timeout: 45000 });

    // The containers tab polls every 5s (TAB_REFRESH.containers.ms) — but the
    // periodic timer's next-fire time is inherited from whichever tab was
    // active when it was last (re)scheduled, so the very first tick after
    // login can still be riding the 8s dashboard interval. Give it comfortable
    // room for that rather than clicking Refresh.
    await expect(otherRow.locator(".badge-muted"), "the idle second session must pick up the stop on its own poll")
      .toBeVisible({ timeout: 20000 });
  } finally {
    await otherCtx.close();
  }

  // Leave the container running again for whatever runs after this test.
  await row.locator('button[data-act="start"]:not([disabled])').click();
  await expect(row.locator(".badge-ok")).toBeVisible({ timeout: 45000 });

  h.assertClean("the cross-session container poll");
});

test("dashboard: another session's container change updates the stat tile without switching away", async ({ page, browser }) => {
  test.setTimeout(60000);
  test.skip(!fixture.hasAdminContainer, "Docker did not provide a container to act on");
  const h = installPage(page);
  await login(page, server.adminUser, server.adminPassword);
  await openTab(page, "dashboard");
  const runningSub = page.locator(".dash-tiles .stat-tile").first().locator(".stat-sub");
  const before = await runningSub.textContent();

  const otherCtx = await browser.newContext();
  const otherPage = await otherCtx.newPage();
  try {
    await login(otherPage, server.adminUser, server.adminPassword);
    await openTab(otherPage, "containers");
    const otherRow = otherPage.locator("#view tbody tr", { hasText: `e2e-admin-${fixture.runId}` });
    await otherRow.locator('button[data-act="stop"]:not([disabled])').click();
    await expect(otherRow.locator(".badge-muted")).toBeVisible({ timeout: 45000 });

    // Still parked on Dashboard the whole time — no tab switch, no Refresh —
    // the 8s dashboard poll (TAB_REFRESH.dashboard.ms) is what has to notice.
    await expect(runningSub, "the running count must drop on the dashboard's own poll")
      .not.toHaveText(before, { timeout: 20000 });

    await otherRow.locator('button[data-act="start"]:not([disabled])').click();
    await expect(otherRow.locator(".badge-ok")).toBeVisible({ timeout: 45000 });
  } finally {
    await otherCtx.close();
  }

  h.assertClean("the cross-page dashboard poll");
});

test("containers: leaving the tab and coming back shows a change made outside the UI right away", async ({ page }) => {
  test.setTimeout(30000);
  test.skip(!fixture.hasAdminContainer, "Docker did not provide a container to act on");
  const h = installPage(page);
  await login(page, server.adminUser, server.adminPassword);
  await openTab(page, "containers");
  const row = page.locator("#view tbody tr", { hasText: `e2e-admin-${fixture.runId}` });
  await expect(row.locator(".badge-ok")).toBeVisible();
  const id = await row.locator('button[data-act="stop"]').getAttribute("data-id");

  // Stand in for a change made outside this page entirely (another device, a
  // bare `docker stop`, a CLI script) — a raw API call that never runs any of
  // this page's own click handlers or render calls.
  const csrf = (await page.context().cookies()).find((c) => c.name === "mudp_csrf")?.value || "";
  const resp = await page.request.post("/api/containers/action", {
    headers: { "X-CSRF-Token": csrf },
    data: { id, action: "stop" },
  });
  expect(resp.ok()).toBeTruthy();

  // Switching tabs fires refreshActiveTab() immediately (app.js's data-tab
  // click handler), so the row must already be current well before the next
  // scheduled 5s poll would have caught it on its own.
  await openTab(page, "dashboard");
  await openTab(page, "containers");
  await expect(row.locator(".badge-muted"), "returning to the tab must show the externally-made change promptly")
    .toBeVisible({ timeout: 4000 });

  await row.locator('button[data-act="start"]:not([disabled])').click();
  await expect(row.locator(".badge-ok")).toBeVisible({ timeout: 45000 });

  h.assertClean("the tab-switch refresh");
});
