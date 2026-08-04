// Which disks the netdisk page offers, and to whom.
//
// The backup disk (备份盘) and shared disk (共享盘) are both per-group settings
// (groups.backup_path / groups.shared_disk_path). With no path configured the
// server can only answer "not configured", so the UI hides those modes rather
// than offering a tab that always errors — and hides them as copy/move
// destinations, as a settings card, and as a create-wizard checkbox too.
//
// The regression this spec exists to catch is the opposite failure: hiding a
// disk from an admin who should still reach it. In particular an admin whose
// own groups have no shared disk still gets the pool via the fallback in
// sharedDiskGroup() — admins may already create/rename/delete anywhere inside
// a pool and configure the group paths themselves, so gating the tab purely on
// their group membership would make the feature unreachable for the one role
// meant to administer it.
//
// This spec owns its own server because every test rewrites group disk paths;
// shared-disk.spec.js covers what the shared disk *does* once it is visible.
//
// Same safety model as the rest of the suite — see fixtures/ui.js.

import { test, expect } from "@playwright/test";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { startServer, seed, apiClient } from "./fixtures/server.js";
import { installPage, login, openTab, closeModals } from "./fixtures/ui.js";

const PORT = 19106;
test.use({ baseURL: `http://127.0.0.1:${PORT}` });

test.describe.configure({ mode: "serial" });

let server;
let fixture;
let adminApi;
let usersGroupId;
// Scratch roots handed to the group disk settings. Registered here (rather than
// per test) so afterAll can remove them even when a test fails midway.
const scratchRoots = [];

const netdiskTab = '.netdisk-mode-btn[data-mode="netdisk"]';
const backupTab = '.netdisk-mode-btn[data-mode="backup"]';
const sharedTab = '.netdisk-mode-btn[data-mode="shareddisk"]';

// scratchRoot makes a throwaway directory for a group disk path and remembers
// it for cleanup.
function scratchRoot(label) {
  const dir = path.join(os.tmpdir(), `mudp-e2e-${label}-${PORT}-${Date.now()}`);
  fs.mkdirSync(dir, { recursive: true });
  scratchRoots.push(dir);
  return dir;
}

// setGroupDisk points a group's backup/shared root somewhere, or clears it with
// "". Throws rather than returning a flag: a silently failed setup would make
// the assertions below test the wrong state.
async function setGroupDisk(kind, groupId, dir) {
  const res = await adminApi.post(`/api/groups/${kind}`, { groupId, path: dir });
  if (!res.ok) throw new Error(`setting the group ${kind} path failed: ${res.body}`);
}

test.beforeAll(async () => {
  server = await startServer({ port: PORT });
  fixture = await seed(server);
  adminApi = await apiClient(server.url, server.adminUser, server.adminPassword);
  const groups = (await adminApi.get("/api/groups")) || [];
  usersGroupId = groups.find((g) => g.name === "users")?.id;
  if (!usersGroupId) throw new Error("default 'users' group missing from a fresh database");
});

test.afterAll(async () => {
  if (adminApi) await adminApi.dispose();
  if (fixture) await fixture.cleanup();
  if (server) await server.stop();
  for (const dir of scratchRoots) {
    try { fs.rmSync(dir, { recursive: true, force: true }); } catch { /* best effort */ }
  }
});

test("with neither disk configured, only the netdisk is offered anywhere", async ({ page }) => {
  test.setTimeout(120000);
  const h = installPage(page);
  // seed() sets a netdisk root but no backup or shared-disk root, so this is
  // the state a fresh install starts in.
  await login(page, server.adminUser, server.adminPassword);
  await openTab(page, "netdisk");

  await expect(page.locator(netdiskTab)).toHaveClass(/active/);
  await expect(page.locator(backupTab)).toHaveCount(0);
  await expect(page.locator(sharedTab)).toHaveCount(0);

  // Not offered as a copy/move destination either — there is nowhere to put
  // anything on a disk with no root.
  const folder = `disk-tabs-${fixture.runId}`;
  await h.withConfirm(() => page.click("#mkdirBtn"), folder);
  await expect(page.locator("#view tbody tr", { hasText: folder })).toBeVisible({ timeout: 20000 });
  await page.check("#selectAllFiles");
  await page.click("#batchCopy");
  await expect(page.locator(".modal-backdrop.netdisk-picker")).toBeVisible({ timeout: 20000 });
  await expect(page.locator('[data-picker-disk="netdisk"]')).toHaveCount(1);
  await expect(page.locator('[data-picker-disk="backup"]')).toHaveCount(0);
  await expect(page.locator('[data-picker-disk="shareddisk"]')).toHaveCount(0);
  await closeModals(page);

  // The shared-disk access preference has no folder to govern, so its settings
  // card is omitted rather than shown with nothing behind it.
  // The settings page lives under the "scripts" tab key (see app.js).
  await openTab(page, "scripts");
  await expect(page.locator("#userLanguageForm")).toBeVisible();
  await expect(page.locator("#sharedDiskAccessForm")).toHaveCount(0);

  h.assertClean("the netdisk page with no backup or shared disk configured");
});

test("configuring the backup disk brings its tab back for an admin", async ({ page }) => {
  test.setTimeout(120000);
  const h = installPage(page);
  await setGroupDisk("backup", usersGroupId, scratchRoot("backup"));

  await login(page, server.adminUser, server.adminPassword);
  await openTab(page, "netdisk");

  // Visible, switchable, and genuinely in backup mode — upload is netdisk-only,
  // so its absence proves the view actually changed rather than the tab merely
  // rendering. The shared disk is still unconfigured and must stay hidden.
  await expect(page.locator(backupTab)).toHaveCount(1);
  await expect(page.locator(sharedTab)).toHaveCount(0);
  await page.click(backupTab);
  await expect(page.locator(backupTab)).toHaveClass(/active/);
  await expect(page.locator("#uploadFiles")).toHaveCount(0);
  await page.click(netdiskTab);
  await expect(page.locator("#uploadFiles")).toHaveCount(1);

  // And it becomes a copy/move destination again.
  await page.check("#selectAllFiles");
  await page.click("#batchCopy");
  await expect(page.locator(".modal-backdrop.netdisk-picker")).toBeVisible({ timeout: 20000 });
  await expect(page.locator('[data-picker-disk="backup"]')).toHaveCount(1);
  await closeModals(page);

  h.assertClean("the netdisk page with a backup disk configured");
});

test("configuring the shared disk on the admin's own group brings its tab back", async ({ page }) => {
  test.setTimeout(120000);
  const h = installPage(page);
  await setGroupDisk("shareddisk", usersGroupId, scratchRoot("shareddisk"));

  await login(page, server.adminUser, server.adminPassword);
  await openTab(page, "netdisk");

  await expect(page.locator(sharedTab)).toHaveCount(1);
  await page.click(sharedTab);
  await expect(page.locator(sharedTab)).toHaveClass(/active/);
  // The hint banner only renders in shared-disk mode, so it confirms the switch.
  await expect(page.locator(".netdisk-backup-hint")).toBeVisible();

  // The access preference now has a folder to govern, so its card is back.
  // The settings page lives under the "scripts" tab key (see app.js).
  await openTab(page, "scripts");
  await expect(page.locator("#sharedDiskAccessForm")).toBeVisible();

  h.assertClean("the netdisk page with a shared disk on the admin's own group");
});

test("an admin whose own group has no shared disk still reaches the pool that exists", async ({ page }) => {
  test.setTimeout(120000);
  const h = installPage(page);

  // Move the shared disk onto a group the admin does not belong to: create a
  // fresh group with its own root, then clear the root on "users" (the admin's
  // group). Resolving purely by group membership would now leave the admin with
  // nothing — the fallback in sharedDiskGroup() is what keeps the pool reachable.
  const otherRoot = scratchRoot("shareddisk-other");
  const otherGroupName = `sd-other-${fixture.runId}`;
  const created = await adminApi.post("/api/groups", { name: otherGroupName });
  if (!created.ok) throw new Error(`creating the second group failed: ${created.body}`);
  const groups = (await adminApi.get("/api/groups")) || [];
  const otherGroupId = groups.find((g) => g.name === otherGroupName)?.id;
  if (!otherGroupId) throw new Error(`group ${otherGroupName} missing after creation`);

  try {
    await setGroupDisk("shareddisk", otherGroupId, otherRoot);
    await setGroupDisk("shareddisk", usersGroupId, "");

    await login(page, server.adminUser, server.adminPassword);
    await openTab(page, "netdisk");

    await expect(page.locator(sharedTab), "an admin must not lose the shared disk").toHaveCount(1);
    await page.click(sharedTab);
    await expect(page.locator(sharedTab)).toHaveClass(/active/);
    await expect(page.locator(".netdisk-backup-hint")).toBeVisible();

    // Browsing really resolved to the other group's pool: the server creates the
    // caller's own subfolder on first browse, so it appears on disk under that
    // root (and not under the one "users" used to point at).
    await expect
      .poll(() => fs.readdirSync(otherRoot).length, { timeout: 20000 })
      .toBeGreaterThan(0);

    h.assertClean("the shared disk reached through the admin fallback");
  } finally {
    // Put "users" back so a later run of this file starts from a known state.
    await setGroupDisk("shareddisk", otherGroupId, "");
  }
});

test("a non-admin gets no such fallback — an unconfigured group means no shared disk", async ({ page }) => {
  test.setTimeout(120000);
  const h = installPage(page);
  // Set explicitly rather than relying on the previous test's teardown: the
  // regular user has no claim on another group's pool, so unlike the admin they
  // see nothing — this is the half of the fallback that must NOT be widened.
  await setGroupDisk("shareddisk", usersGroupId, "");
  await login(page, fixture.user.username, fixture.user.password);
  await openTab(page, "netdisk");

  await expect(page.locator(netdiskTab)).toHaveClass(/active/);
  await expect(page.locator(sharedTab)).toHaveCount(0);

  // The settings page lives under the "scripts" tab key (see app.js).
  await openTab(page, "scripts");
  await expect(page.locator("#sharedDiskAccessForm")).toHaveCount(0);

  h.assertClean("the netdisk page for a user whose group has no shared disk");
});
