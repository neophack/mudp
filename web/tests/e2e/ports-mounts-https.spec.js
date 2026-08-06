// Coverage for three container-wizard features not exercised elsewhere:
//
//   1. Multiple explicit port bindings in one container — the "ports" textarea
//      accepts several lines, and every one of them must show up both in the
//      container list row and in its inspect (Details) panel, not just the
//      first.
//   2. A shared-disk bind mount's real ro/rw mode rendered by the Details
//      modal itself (detailRow's "(bind, ro/rw)" suffix) — shared-disk.spec.js
//      proves the *permission rules* via the raw Docker inspect, but nothing
//      exercises the modal text a user actually reads.
//   3. HTTPS-terminated port forwarding (store.ImagePreset.HTTPS8080/8090):
//      an image preset opting a port into TLS termination, on a forwarding
//      network, must surface as a "🔒" connect link on the container row.
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

const PORT = 19108;
test.use({ baseURL: `http://127.0.0.1:${PORT}` });

test.describe.configure({ mode: "serial" });

let server;
let fixture;
let adminApi;

test.beforeAll(async () => {
  server = await startServer({ port: PORT });
  fixture = await seed(server);
  adminApi = await apiClient(server.url, server.adminUser, server.adminPassword);
});

test.afterAll(async () => {
  if (adminApi) await adminApi.dispose();
  if (fixture) await fixture.cleanup();
  if (server) await server.stop();
});

test("the New Container wizard binds multiple explicit ports, and every mapping shows up in the list and the inspect panel", async ({ page }) => {
  test.setTimeout(120000);
  test.skip(!fixture.imagePublished, "no image was published for the container wizard to use");
  const h = installPage(page);
  await login(page, server.adminUser, server.adminPassword);
  await openTab(page, "containers");

  // ":<port>" (no host side) auto-allocates a host port from the caller's
  // assigned range — the same shorthand applyPreset uses — so the test needs
  // no knowledge of the admin's actual port-prefix window.
  const containerName = `ui-multiport-${fixture.runId}`;
  await page.click("#newContainerBtn");
  await page.fill("#newContainer [name='name']", containerName);
  await page.selectOption("#newContainer [name='image']", fixture.imageName);
  await page.fill("#newContainer [name='ports']", ":8081\n:8082");
  await page.click("#createSubmit");

  await expect(page.locator("#newContainer")).toHaveCount(0, { timeout: 60000 });
  const row = page.locator("#view tbody tr", { hasText: containerName });
  await expect(row).toBeVisible({ timeout: 30000 });

  // Both mappings must appear in the row's port line, on two distinct host
  // ports — proving the second line wasn't silently dropped or overwritten
  // by the first.
  const portLine = row.locator(".secondary-line").first();
  await expect(portLine).toContainText("8081/tcp");
  await expect(portLine).toContainText("8082/tcp");
  const lineText = (await portLine.textContent()) || "";
  const hostPort8081 = lineText.match(/(\d+):8081\/tcp/)?.[1];
  const hostPort8082 = lineText.match(/(\d+):8082\/tcp/)?.[1];
  expect(hostPort8081, `expected a host port for 8081 in "${lineText}"`).toBeTruthy();
  expect(hostPort8082, `expected a host port for 8082 in "${lineText}"`).toBeTruthy();
  expect(hostPort8081).not.toBe(hostPort8082);

  // The inspect panel's Ports row is built independently (from the real
  // Docker inspect, not the row's own rendering) and must agree.
  await row.locator('button[data-act="inspect"]').click();
  await expect(page.locator(".modal-backdrop.detail-modal")).toBeVisible({ timeout: 20000 });
  await expect(page.locator(".detail")).toContainText(`${hostPort8081}:8081/tcp`);
  await expect(page.locator(".detail")).toContainText(`${hostPort8082}:8082/tcp`);
  await closeModals(page);

  h.assertClean("the multi-port-binding container workflow");
});

test("a shared-disk mount's real read-only/read-write mode is rendered by the Details modal, not just provable via raw inspect", async ({ page }) => {
  test.setTimeout(120000);
  test.skip(!fixture.imagePublished, "no image was published for the container wizard to use");
  const h = installPage(page);

  // Point the group's shared-disk root at a scratch dir and pre-create the
  // user's own folder — same setup shared-disk.spec.js's beforeAll does —
  // scoped to just this one test since nothing else in this file touches it.
  const groups = (await adminApi.get("/api/groups")) || [];
  const usersGroup = groups.find((g) => g.name === "users");
  const sharedDiskRoot = path.join(os.tmpdir(), `mudp-e2e-sd-modal-${PORT}-${Date.now()}`);
  fs.mkdirSync(sharedDiskRoot, { recursive: true });
  const sdRes = await adminApi.post("/api/groups/shareddisk", { groupId: usersGroup.id, path: sharedDiskRoot });
  if (!sdRes.ok) throw new Error(`setting the group shared-disk path failed: ${sdRes.body}`);

  const userApi = await apiClient(server.url, fixture.user.username, fixture.user.password);
  const userMe = await userApi.get("/api/me");
  const userFolderName = `${fixture.user.username}-${userMe?.id ?? ""}`;
  fs.mkdirSync(path.join(sharedDiskRoot, userFolderName), { recursive: true });

  try {
    await login(page, fixture.user.username, fixture.user.password);
    await openTab(page, "containers");

    // sharedDiskMountsFor always emits the whole-pool base mount at /data
    // read-only, then overlays the caller's own folder read-write on top —
    // so a single container exercises both modes in one Details panel.
    const containerName = `ui-sd-modal-${fixture.runId}`;
    await page.click("#newContainerBtn");
    await page.fill("#newContainer [name='name']", containerName);
    await page.selectOption("#newContainer [name='image']", fixture.imageName);
    await expect(page.locator("#sharedDiskSection")).toBeVisible();
    await page.locator("#sharedDiskSection label.check").click();
    await expect(page.locator("#sharedDiskSection [name='mountSharedDisk']")).toBeChecked();
    await page.click("#createSubmit");

    await expect(page.locator("#newContainer")).toHaveCount(0, { timeout: 60000 });
    const row = page.locator("#view tbody tr", { hasText: containerName });
    await expect(row).toBeVisible({ timeout: 30000 });

    await row.locator('button[data-act="inspect"]').click();
    await expect(page.locator(".modal-backdrop.detail-modal")).toBeVisible({ timeout: 20000 });
    const detail = page.locator(".detail");
    await expect(detail).toContainText("→ /data (bind, ro)");
    await expect(detail).toContainText(`→ /data/${userFolderName} (bind, rw)`);
    await closeModals(page);

    h.assertClean("the shared-disk ro/rw Details-modal rendering");
  } finally {
    await userApi.dispose();
    try { fs.rmSync(sharedDiskRoot, { recursive: true, force: true }); } catch { /* best effort */ }
  }
});

test("an image preset's HTTPS-terminated forward surfaces as a lock-icon connect link on the container row", async ({ page }) => {
  test.setTimeout(120000);
  test.skip(!fixture.imagePublished, "no image was published for the container wizard to use");
  const h = installPage(page);

  // HTTPS termination (docker.go: opts.HTTPS8080 && forwardNet != "") only
  // ever applies on a network an admin has flagged for mudp forwarding —
  // Docker itself cannot terminate TLS. Set both preconditions through the
  // API, the same way the rest of the suite drives non-UI setup steps
  // (shared-disk.spec.js's group paths, disk-tabs.spec.js's group disks).
  const netName = `e2e-https-net-${fixture.runId}`;
  const netRes = await adminApi.post("/api/networks", { name: netName, driver: "bridge" });
  if (!netRes.ok) throw new Error(`creating the forwarding network failed: ${netRes.body}`);
  const net = ((await adminApi.get("/api/networks")) || []).find((n) => n.name === netName);
  if (!net) throw new Error(`created network "${netName}" did not appear in the network list`);
  const fwdRes = await adminApi.post("/api/networks/forward", { name: net.fullName || net.name, forward: true });
  if (!fwdRes.ok) throw new Error(`enabling forwarding on the network failed: ${fwdRes.body}`);

  const image = ((await adminApi.get("/api/images")) || []).find((i) => i.name === fixture.imageName);
  if (!image) throw new Error(`seeded image "${fixture.imageName}" not found`);
  const presetRes = await adminApi.post("/api/images/preset", { imageId: image.id, preset: { https8080: true } });
  if (!presetRes.ok) throw new Error(`setting the image preset failed: ${presetRes.body}`);

  try {
    await login(page, server.adminUser, server.adminPassword);
    await openTab(page, "containers");

    const containerName = `ui-https-fwd-${fixture.runId}`;
    await page.click("#newContainerBtn");
    await page.fill("#newContainer [name='name']", containerName);
    await page.selectOption("#newContainer [name='image']", fixture.imageName);
    // The network checkbox's real hit target is the label (see shared-disk.spec.js);
    // the underlying input is visually hidden.
    const netLabel = page.locator("#newContainer label.check", { hasText: netName });
    await netLabel.click();
    await expect(netLabel.locator('input[name="networks"]')).toBeChecked();
    await page.click("#createSubmit");

    await expect(page.locator("#newContainer")).toHaveCount(0, { timeout: 60000 });
    const row = page.locator("#view tbody tr", { hasText: containerName });
    await expect(row).toBeVisible({ timeout: 30000 });

    // containers.js only ever emits this link when c.https8080Url is set,
    // which docker.go only ever populates from a real TLS-forward label —
    // so its presence proves the preset flag made it all the way through
    // container creation into what the row renders.
    const httpsLink = row.locator("a.port-link", { hasText: "🔒" });
    await expect(httpsLink).toBeVisible({ timeout: 20000 });
    await expect(httpsLink).toContainText("8080");
    await expect(httpsLink).toHaveAttribute("href", /^https:\/\//);

    h.assertClean("the HTTPS-forwarded container's connect link");
  } finally {
    // Best-effort teardown so a failure mid-test doesn't leave the network
    // stuck in forwarding mode for the rest of this file's (already-finished)
    // run — the server itself is thrown away in afterAll regardless.
    await adminApi.post("/api/networks/forward", { name: net.fullName || net.name, forward: false });
  }
});
