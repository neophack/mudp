// Shared-disk (共享盘) end-to-end coverage: file-browser button states,
// the self-service read-only/read-write setting, and the container-side bind
// permission that setting controls.
//
// The volume-mount workflow in user-workflows.spec.js already proves a named
// volume mount round-trips into the inspect panel. The backend rejects raw
// host bind mounts (a user must use the netdisk option or a managed volume),
// so the wizard's mounts field only ever carries named volumes — that path is
// covered there, not here. This spec covers the shared disk specifically:
//
//   1. The shared-disk checkbox binds the caller's own folder at /data and the
//      real mount shows up in the inspect panel.
//   2. In the shared-disk file browser, a user can edit (move/rename/delete)
//      only their own folder; everyone else's shows copy-only. An admin can
//      edit every folder.
//   3. The settings "共享盘权限" select persists read-only/read-write. That
//      preference does NOT affect a user's own folder in their own container
//      (always read-write); it governs how OTHER members' containers mount the
//      folder. An admin's container overrides every owner to read-write. All
//      verified through the raw Docker inspect's RW flag, not the modal (which
//      omits the mode).
//
// Same safety model as the rest of the suite — see fixtures/ui.js: native
// confirm()/prompt() dialogs are cancelled unless a test opts in via
// withConfirm().

import { test, expect } from "@playwright/test";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { startServer, seed, apiClient } from "./fixtures/server.js";
import { installPage, login, openTab, closeModals } from "./fixtures/ui.js";

const PORT = 19105;
test.use({ baseURL: `http://127.0.0.1:${PORT}` });

test.describe.configure({ mode: "serial" });

let server;
let fixture;
// The group shared-disk root the beforeAll configures, plus the folder names
// the server derives for the two accounts (pinyin(displayName|username) + "-"
// + userId). The container-side mount target is /data/<folderName>.
let sharedDiskRoot;
let adminFolderName;
let userFolderName;
// Logged-in API clients kept around for tests that need to drive the server
// directly (toggling the read-write preference, reading the raw inspect).
let adminApi;
let userApi;

test.beforeAll(async () => {
  server = await startServer({ port: PORT });
  fixture = await seed(server);

  // seed() wires the group netdisk path but not the shared-disk path; without
  // it the create wizard's shared-disk bind is a no-op (the server has nowhere
  // to bind from). Set it here on a throwaway scratch dir, the same way seed()
  // provisions the netdisk root.
  adminApi = await apiClient(server.url, server.adminUser, server.adminPassword);
  const groups = (await adminApi.get("/api/groups")) || [];
  const usersGroup = groups.find((g) => g.name === "users");
  sharedDiskRoot = path.join(os.tmpdir(), `mudp-e2e-shareddisk-${PORT}-${Date.now()}`);
  fs.mkdirSync(sharedDiskRoot, { recursive: true });
  const sdRes = await adminApi.post("/api/groups/shareddisk", { groupId: usersGroup.id, path: sharedDiskRoot });
  if (!sdRes.ok) throw new Error(`setting the group shared-disk path failed: ${sdRes.body}`);

  // Resolve both accounts' folder names so tests can assert exact /data paths
  // and locate the right rows in the file browser. The folder name the server
  // derives is pinyin(displayName|username) + "-" + userId; for ASCII usernames
  // pinyin is a no-op, so it's "<username>-<id>".
  const adminMe = await adminApi.get("/api/me");
  adminFolderName = `${server.adminUser}-${adminMe?.id ?? ""}`;
  userApi = await apiClient(server.url, fixture.user.username, fixture.user.password);
  const userMe = await userApi.get("/api/me");
  userFolderName = `${fixture.user.username}-${userMe?.id ?? ""}`;

  // Materialize both folders under the pool root so the file browser lists two
  // rows. The server creates a user's own folder on demand when they browse,
  // but creating both up front lets the admin-side button-state test see the
  // user's folder without first logging in as that user.
  fs.mkdirSync(path.join(sharedDiskRoot, adminFolderName), { recursive: true });
  fs.mkdirSync(path.join(sharedDiskRoot, userFolderName), { recursive: true });
});

test.afterAll(async () => {
  if (userApi) await userApi.dispose();
  if (adminApi) await adminApi.dispose();
  if (fixture) await fixture.cleanup();
  if (server) await server.stop();
  // The server's stop() only removes netdiskRoot; clean up the shared-disk
  // scratch dir we created here too.
  try { fs.rmSync(sharedDiskRoot, { recursive: true, force: true }); } catch { /* best effort */ }
});

test("checking 'mount shared disk' binds the caller's own folder at /data and it appears in the inspect panel", async ({ page }) => {
  test.setTimeout(120000);
  test.skip(!fixture.imagePublished, "no image was published for the container wizard to use");
  const h = installPage(page);
  await login(page, server.adminUser, server.adminPassword);

  await openTab(page, "containers");
  const containerName = `ui-shareddisk-${fixture.runId}`;
  await page.click("#newContainerBtn");
  await page.fill("#newContainer [name='name']", containerName);
  await page.selectOption("#newContainer [name='image']", fixture.imageName);

  // The shared-disk section is no longer gated on an image preset — it shows
  // for every image now that beforeAll has given the group a shared-disk root.
  // (admin-console.spec.js covers the unconfigured case, where it is absent.)
  await expect(page.locator("#sharedDiskSection")).toBeVisible();
  // label.check inputs are visually hidden (position:absolute; width/height:0
  // in styles.css) — the visible box is drawn on the label via ::before. So we
  // click the label (the real hit target) and then assert the linked input's
  // checked state, instead of driving the zero-size input directly.
  const sharedDiskLabel = page.locator("#sharedDiskSection label.check");
  const sharedDiskCheckbox = page.locator("#sharedDiskSection [name='mountSharedDisk']");
  await sharedDiskLabel.click();
  await expect(sharedDiskCheckbox).toBeChecked();
  await page.click("#createSubmit");

  await expect(page.locator("#newContainer")).toHaveCount(0, { timeout: 60000 });
  const row = page.locator("#view tbody tr", { hasText: containerName });
  await expect(row).toBeVisible({ timeout: 30000 });

  // The container-side mount target is /data/<folderName>. Asserting the exact
  // folder (not just /data) guards against the bind being silently dropped or
  // pointed at the wrong owner's folder.
  await row.locator('button[data-act="inspect"]').click();
  await expect(page.locator(".modal-backdrop.detail-modal")).toBeVisible({ timeout: 20000 });
  await expect(page.locator(".detail")).toContainText(`/data/${adminFolderName}`);
  await closeModals(page);

  h.assertClean("the shared-disk mount container workflow");
});

test("shared-disk file browser: a regular user can edit only their own folder, everyone else's is copy-only", async ({ page }) => {
  test.setTimeout(120000);
  const h = installPage(page);
  await login(page, fixture.user.username, fixture.user.password);
  await openTab(page, "netdisk");

  // Switch into shared-disk mode. The mode button must become active and the
  // shared-disk hint banner must render — both confirm the view actually
  // switched rather than the click having missed.
  await page.click('.netdisk-mode-btn[data-mode="shareddisk"]');
  await expect(page.locator('.netdisk-mode-btn[data-mode="shareddisk"]')).toHaveClass(/active/);
  await expect(page.locator(".netdisk-backup-hint")).toBeVisible();

  // The root lists one row per member folder. Both the user's own folder and
  // the admin's must be present.
  const ownRow = page.locator("#view tbody tr", { hasText: userFolderName });
  const otherRow = page.locator("#view tbody tr", { hasText: adminFolderName });
  await expect(ownRow).toBeVisible({ timeout: 20000 });
  await expect(otherRow).toBeVisible();

  // Own folder: the full edit suite (copy + move + rename + delete) is
  // available. On the desktop viewport the row menu is laid out flat
  // (display:contents), so these buttons are directly present and visible —
  // no toggle to open first.
  await expect(ownRow.locator("button[data-copy]")).toBeVisible();
  await expect(ownRow.locator("button[data-move]")).toBeVisible();
  await expect(ownRow.locator("button[data-ren]")).toBeVisible();
  await expect(ownRow.locator("button[data-del]")).toBeVisible();

  // Other user's folder: only copy is offered; move/rename/delete are absent
  // (not disabled — the DOM simply omits them), enforcing the own-folder-only
  // mutation rule client-side. The backend re-checks via requireOwnSharedDiskPath.
  await expect(otherRow.locator("button[data-copy]")).toBeVisible();
  await expect(otherRow.locator("button[data-move]")).toHaveCount(0);
  await expect(otherRow.locator("button[data-ren]")).toHaveCount(0);
  await expect(otherRow.locator("button[data-del]")).toHaveCount(0);

  h.assertClean("the shared-disk file browser button states for a regular user");
});

test("shared-disk file browser: an admin can edit every folder", async ({ page }) => {
  test.setTimeout(120000);
  const h = installPage(page);
  await login(page, server.adminUser, server.adminPassword);
  await openTab(page, "netdisk");

  await page.click('.netdisk-mode-btn[data-mode="shareddisk"]');
  await expect(page.locator('.netdisk-mode-btn[data-mode="shareddisk"]')).toHaveClass(/active/);

  // An admin sees the full edit suite on EVERY row, including the user's
  // folder — the own-folder restriction does not apply to admins.
  const userFolderRow = page.locator("#view tbody tr", { hasText: userFolderName });
  await expect(userFolderRow).toBeVisible({ timeout: 20000 });
  await expect(userFolderRow.locator("button[data-copy]")).toBeVisible();
  await expect(userFolderRow.locator("button[data-move]")).toBeVisible();
  await expect(userFolderRow.locator("button[data-ren]")).toBeVisible();
  await expect(userFolderRow.locator("button[data-del]")).toBeVisible();

  h.assertClean("the shared-disk file browser button states for an admin");
});

test("copy/move picker browses into a shared-disk subfolder, not just the own-folder root", async ({ page }) => {
  test.setTimeout(120000);
  const h = installPage(page);
  await login(page, server.adminUser, server.adminPassword);
  await openTab(page, "netdisk");

  // Make a folder on the netdisk to copy from.
  const sourceFolder = `to-shared-${fixture.runId}`;
  await h.withConfirm(() => page.click("#mkdirBtn"), sourceFolder);
  await expect(page.locator("#view tbody tr", { hasText: sourceFolder })).toBeVisible({ timeout: 20000 });
  await page.check("#selectAllFiles");

  // Open the copy destination picker and switch to the shared disk.
  await page.click("#batchCopy");
  await expect(page.locator(".modal-backdrop.netdisk-picker")).toBeVisible({ timeout: 20000 });
  await page.click('[data-picker-disk="shareddisk"]');

  // The picker must list the pool's member folders (browsable, not the old
  // "fixed destination" empty state) and let us descend into the admin's own
  // folder. The regression this guards: the picker used to pin the target at
  // the own-folder root with no way to choose a subfolder inside it.
  const adminRow = page.locator("#pickerList .picker-row", { hasText: adminFolderName });
  await expect(adminRow).toBeVisible({ timeout: 20000 });
  await adminRow.click();

  // Create a subfolder inside the admin's own folder and target it. The
  // destination hint reflects the full nested path, proving the chosen
  // subfolder — not the own-folder root — is what the transfer will use.
  const subFolder = `dest-${fixture.runId}`;
  await h.withConfirm(() => page.click("#pickerMkdir"), subFolder);
  const subRow = page.locator("#pickerList .picker-row", { hasText: subFolder });
  await expect(subRow).toBeVisible({ timeout: 20000 });
  await subRow.click();
  await expect(page.locator("#pickerHint")).toContainText(`${adminFolderName}/${subFolder}`);

  await closeModals(page);
  h.assertClean("the shared-disk subfolder copy picker");
});

test("a regular user's copy picker only shows their own shared folder, not other members'", async ({ page }) => {
  test.setTimeout(120000);
  const h = installPage(page);
  await login(page, fixture.user.username, fixture.user.password);
  await openTab(page, "netdisk");

  // Make a folder on the netdisk to copy from.
  const sourceFolder = `to-shared-user-${fixture.runId}`;
  await h.withConfirm(() => page.click("#mkdirBtn"), sourceFolder);
  await expect(page.locator("#view tbody tr", { hasText: sourceFolder })).toBeVisible({ timeout: 20000 });
  await page.check("#selectAllFiles");

  // Open the copy destination picker and switch to the shared disk.
  await page.click("#batchCopy");
  await expect(page.locator(".modal-backdrop.netdisk-picker")).toBeVisible({ timeout: 20000 });
  await page.click('[data-picker-disk="shareddisk"]');

  // The picker roots the user INSIDE their own shared folder: its own
  // subfolders are listed and browsable, but other members' folders (the
  // admin's here) never appear — a regular user can't write to them, so
  // showing them would only lead to a server-side rejection.
  await expect(page.locator("#pickerPath")).toContainText(userFolderName);
  await expect(page.locator("#pickerList .picker-row", { hasText: adminFolderName })).toHaveCount(0);
  // "Up" is disabled at the own-folder root so they can't climb out of it.
  await expect(page.locator("#pickerUp")).toBeDisabled();

  await closeModals(page);
  h.assertClean("the shared-disk copy picker fenced to a regular user's own folder");
});

test("the settings 'shared disk access' select persists read-only and read-write", async ({ page }) => {
  test.setTimeout(60000);
  const h = installPage(page);
  await login(page, fixture.user.username, fixture.user.password);
  // A non-admin reaches the settings page under the "scripts" tab (the admin
  // only "settings" tab is not in their nav — see app.js tabs list). Both render
  // renderSettings(); only the shared-disk-access card matters here.
  await openTab(page, "scripts");

  const select = page.locator("#sharedDiskAccessSelect");
  await expect(select).toBeVisible();
  // Fresh user defaults to read-only ("ro").
  await expect(select).toHaveValue("ro");

  // Switch to read-write and save — a success toast confirms the round trip.
  await select.selectOption("rw");
  await page.click("#sharedDiskAccessForm button[type='submit']");
  await expect(page.locator(".toast")).toBeVisible({ timeout: 10000 });

  // Persisted server-side: a fresh API read agrees.
  const rw = await userApi.get("/api/user/shareddisk-access");
  expect(rw?.readWrite).toBe(true);

  // Switch back to read-only and confirm it sticks too.
  await select.selectOption("ro");
  await page.click("#sharedDiskAccessForm button[type='submit']");
  await expect(page.locator(".toast")).toBeVisible({ timeout: 10000 });
  const ro = await userApi.get("/api/user/shareddisk-access");
  expect(ro?.readWrite).toBe(false);

  h.assertClean("the shared-disk access settings form");
});

test("a container's folder mount follows the folder owner's read-write preference (and an admin overrides it)", async ({ page }) => {
  test.setTimeout(240000);
  test.skip(!fixture.imagePublished, "no image was published for the container wizard to use");
  const h = installPage(page);

  // The shared-disk bind permission has three rules (shared.go sharedDiskMountsFor):
  //   1. A user's OWN folder is ALWAYS read-write in their OWN container,
  //      regardless of their sharedDiskReadWrite preference — that preference
  //      only governs how OTHER members' containers mount the folder.
  //   2. Another member's folder follows THAT owner's preference: read-only if
  //      they kept read-only, read-write if they opted in.
  //   3. An admin's container gets read-write on every folder, overriding each
  //      owner's preference.
  // All three are verified below through the raw Docker inspect's Mounts[].RW,
  // the authoritative flag for a bind mount (the modal omits the mode).

  // Spin up a second group member to exercise the cross-user rule. The admin
  // account is exempt from the preference entirely, so it can't stand in here.
  const groups = (await adminApi.get("/api/groups")) || [];
  const usersGroup = groups.find((g) => g.name === "users");
  const secondUsername = `e2esd-${fixture.runId}`;
  const secondPassword = "e2e-shareddisk-secret";
  const createSecond = await adminApi.post("/api/users", {
    username: secondUsername, password: secondPassword, role: "user",
    groupIds: [usersGroup.id], containerCap: 5, netdiskQuotaBytes: 2 * 1024 * 1024 * 1024,
  });
  test.skip(!createSecond.ok, "creating the second user failed; cannot verify cross-user mount mode");
  const secondApi = await apiClient(server.url, secondUsername, secondPassword);
  const secondMe = await secondApi.get("/api/me");
  const secondFolderName = `${secondUsername}-${secondMe?.id ?? ""}`;
  // The second user needs a folder present in the pool so it shows up as a bind.
  fs.mkdirSync(path.join(sharedDiskRoot, secondFolderName), { recursive: true });

  try {
    // Rule 1: owner read-only, yet their own folder is read-write in their own
    // container. Setting the preference to read-only first is deliberate — a
    // regression that dropped the "own folder is always rw" exception would
    // surface here as RW: false.
    await userApi.post("/api/user/shareddisk-access", { readWrite: false });
    const ownName = `ui-sd-own-${fixture.runId}`;
    const ownRes = await userApi.post("/api/containers", {
      name: ownName, image: fixture.imageName, command: "sleep 3600",
      mountNetdisk: false, mountSharedDisk: true,
    });
    test.skip(!ownRes.ok, "container creation failed; cannot verify mount mode");
    const ownId = JSON.parse(ownRes.body).id;
    const ownRaw = await rawMounts(userApi, ownId);
    const ownMount = ownRaw.find((m) => m.Destination === `/data/${userFolderName}`);
    expect(ownMount, `expected a mount at /data/${userFolderName} in ${JSON.stringify(ownRaw)}`).toBeTruthy();
    expect(ownMount.RW).toBe(true);

    // Rule 2a: the second user's container mounts the user's folder, which the
    // user has kept read-only → that bind must be read-only.
    const otherRoName = `ui-sd-other-ro-${fixture.runId}`;
    const otherRoRes = await secondApi.post("/api/containers", {
      name: otherRoName, image: fixture.imageName, command: "sleep 3600",
      mountNetdisk: false, mountSharedDisk: true,
    });
    test.skip(!otherRoRes.ok, "container creation failed; cannot verify mount mode");
    const otherRoId = JSON.parse(otherRoRes.body).id;
    const otherRoRaw = await rawMounts(secondApi, otherRoId);
    const otherRoMount = otherRoRaw.find((m) => m.Destination === `/data/${userFolderName}`);
    expect(otherRoMount, `expected a mount at /data/${userFolderName} in ${JSON.stringify(otherRoRaw)}`).toBeTruthy();
    expect(otherRoMount.RW).toBe(false);

    // Rule 2b: the user now opts into read-write; the second user's NEXT
    // container reflects the flip — same folder, now read-write.
    await userApi.post("/api/user/shareddisk-access", { readWrite: true });
    const otherRwName = `ui-sd-other-rw-${fixture.runId}`;
    const otherRwRes = await secondApi.post("/api/containers", {
      name: otherRwName, image: fixture.imageName, command: "sleep 3600",
      mountNetdisk: false, mountSharedDisk: true,
    });
    test.skip(!otherRwRes.ok, "container creation failed; cannot verify mount mode");
    const otherRwId = JSON.parse(otherRwRes.body).id;
    const otherRwRaw = await rawMounts(secondApi, otherRwId);
    const otherRwMount = otherRwRaw.find((m) => m.Destination === `/data/${userFolderName}`);
    expect(otherRwMount, `expected a mount at /data/${userFolderName} in ${JSON.stringify(otherRwRaw)}`).toBeTruthy();
    expect(otherRwMount.RW).toBe(true);

    // Rule 3: an admin's container gets read-write on the user's folder even
    // when the owner kept read-only. Set the preference back to read-only first
    // so the admin override is the only thing that could make it writable.
    await userApi.post("/api/user/shareddisk-access", { readWrite: false });
    const adminName = `ui-sd-admin-${fixture.runId}`;
    const adminRes = await adminApi.post("/api/containers", {
      name: adminName, image: fixture.imageName, command: "sleep 3600",
      mountNetdisk: false, mountSharedDisk: true,
    });
    test.skip(!adminRes.ok, "admin container creation failed; cannot verify mount mode");
    const adminId = JSON.parse(adminRes.body).id;
    const adminRaw = await rawMounts(adminApi, adminId);
    const adminUserMount = adminRaw.find((m) => m.Destination === `/data/${userFolderName}`);
    expect(adminUserMount, `expected a mount at /data/${userFolderName} in admin container`).toBeTruthy();
    expect(adminUserMount.RW).toBe(true);
  } finally {
    await secondApi.dispose();
  }

  // Reset the owner preference so later specs in this serial run start clean.
  await userApi.post("/api/user/shareddisk-access", { readWrite: false });

  // The page object is required by installPage, but this test drives everything
  // through the API; just make sure no errors leaked onto a backgrounded page.
  h.assertClean("the shared-disk mount permission phases");
});

// rawMounts fetches the raw Docker inspect for a container and returns its
// Mounts array, where RW (bool) / Destination / Source reflect the real bind.
async function rawMounts(client, id) {
  const res = await client.ctx.get(`/api/containers/inspect/raw?id=${encodeURIComponent(id)}`);
  if (!res.ok()) throw new Error(`raw inspect failed: ${res.status()} ${await res.text()}`);
  const json = await res.json();
  return json.Mounts || [];
}
