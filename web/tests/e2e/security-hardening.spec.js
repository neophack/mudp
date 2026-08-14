// UI-level security-hardening spec backing docs/SECURITY-AUDIT.md.
//
// The Go suite (internal/server/security_audit_test.go and
// security_regression_test.go) proves the server-side behavior against raw
// HTTP. This spec covers the three surfaces that only a real browser (or a
// logged-out visitor) can exercise end to end:
//
//   1. A password-protected share link: the public /pan/<token> page must gate
//      browsing behind the extraction code, reject a wrong code without
//      leaking anything, and admit the right one.
//   2. Share scope: paths outside the shared set (including traversal-shaped
//      ones) are refused on the public API even with the correct password.
//   3. Chunked upload happy path: the protocol the audit hardened must still
//      work for an honest client, and the init validation that DOES exist
//      (negative size / zero chunkSize / zero totalChunks -> 400) stays in
//      place.
//
// Same safety model as the rest of the suite — see fixtures/ui.js.

import { test, expect } from "@playwright/test";
import { startServer, seed, apiClient } from "./fixtures/server.js";
import { installPage, login, openTab } from "./fixtures/ui.js";

const PORT = 19109;
test.use({ baseURL: `http://127.0.0.1:${PORT}` });

test.describe.configure({ mode: "serial" });

let server;
let fixture;
let admin;

// Shared across the serial tests below.
let shareLink; // absolute /pan/<token> URL created through the real share dialog
let shareToken;
let shareCode;
let shareFileName;

test.beforeAll(async () => {
  server = await startServer({ port: PORT });
  fixture = await seed(server);
  admin = await apiClient(server.url, server.adminUser, server.adminPassword);
});

test.afterAll(async () => {
  if (admin) await admin.dispose();
  if (fixture) await fixture.cleanup();
  if (server) await server.stop();
});

// ---------------------------------------------------------------------------
// 1. Password-protected share — real UI flow, logged-out visitor
// ---------------------------------------------------------------------------

test("share dialog creates a password-protected link; /pan page gates it behind the code", async ({ page, browser }) => {
  test.setTimeout(90000);
  const h = installPage(page);

  // Create the file to share via the API (upload UI is covered elsewhere).
  shareFileName = `sec-hard-${fixture.runId}.txt`;
  const content = `security-hardening content — ${fixture.runId}\n`;
  const up = await admin.ctx.post("/api/netdisk/upload", {
    headers: { "X-CSRF-Token": admin.csrf },
    multipart: {
      path: "",
      files: { name: shareFileName, mimeType: "text/plain", buffer: Buffer.from(content) },
    },
  });
  expect(up.ok(), `upload failed: ${up.status()} ${await up.text()}`).toBe(true);

  // Drive the real share composer: select the row, open Share, enable the
  // extraction code, note the generated code, create the link.
  await login(page, server.adminUser, server.adminPassword);
  await openTab(page, "netdisk");
  const row = page.locator("table.netdisk-table tbody tr", { hasText: shareFileName }).first();
  await expect(row).toBeVisible({ timeout: 20000 });
  await row.locator('button[title="Share"]').click();
  await expect(page.locator(".modal-backdrop.netdisk-share")).toBeVisible();

  await page.check("#shareUseCode");
  shareCode = (await page.locator("#shareCode").inputValue()).trim();
  expect(shareCode.length).toBeGreaterThanOrEqual(4);

  await page.click("#shareCreate");
  await expect(page.locator(".share-copy-chip code").nth(1)).toBeVisible({ timeout: 20000 });
  const relLink = ((await page.locator(".share-copy-chip code").nth(0).textContent()) || "").trim();
  shareToken = relLink.split("/").pop();
  shareLink = `http://127.0.0.1:${PORT}/pan/${shareToken}`;
  h.assertClean("creating a password-protected share");

  // Visit the link from a pristine (logged-out) context: the password
  // backdrop must appear and the file list must stay empty.
  const anon = await browser.newContext();
  const pub = await anon.newPage();
  try {
    await pub.goto(shareLink);
    await expect(pub.locator("#passwordBackdrop")).toBeVisible({ timeout: 20000 });
    await expect(pub.locator("#shareBody")).not.toContainText(shareFileName);

    // Wrong code: still gated, still nothing leaked.
    await pub.fill("#sharePasswordInput", "wrong-code");
    await pub.click("#submitPassword");
    await expect(pub.locator("#passwordBackdrop")).toBeVisible({ timeout: 20000 });
    await expect(pub.locator("#shareBody")).not.toContainText(shareFileName);

    // Correct code: the shared file becomes visible.
    await pub.fill("#sharePasswordInput", shareCode);
    await pub.click("#submitPassword");
    await expect(pub.locator("#shareBody")).toContainText(shareFileName, { timeout: 20000 });
    await expect(pub.locator("#passwordBackdrop")).toBeHidden();

    // And the download behind the link is content-identical.
    const dl = await pub.request.get(
      `http://127.0.0.1:${PORT}/api/netdisk/share/download?token=${encodeURIComponent(shareToken)}&path=${encodeURIComponent(shareFileName)}`,
      { headers: { "X-Share-Password": shareCode } },
    );
    expect(dl.status()).toBe(200);
    expect(await dl.text()).toContain(fixture.runId);
  } finally {
    await anon.close();
  }
});

// ---------------------------------------------------------------------------
// 2. Share scope — traversal and unshared siblings refused on the public API
// ---------------------------------------------------------------------------

test("public share API refuses paths outside the shared set, including traversal shapes", async ({ request }) => {
  test.setTimeout(60000);
  expect(shareToken).toBeTruthy();

  const wrong = await request.get(
    `/api/netdisk/share/public?token=${encodeURIComponent(shareToken)}&path=${encodeURIComponent("../..")}`,
    { headers: { "X-Share-Password": shareCode } },
  );
  // 403 "path is not in this share" (or 401 when the password is required on
  // this route) — anything but a 200 listing.
  expect(wrong.status()).not.toBe(200);

  const sibling = await request.get(
    `/api/netdisk/share/public?token=${encodeURIComponent(shareToken)}&path=${encodeURIComponent("not-shared-at-all.txt")}`,
    { headers: { "X-Share-Password": shareCode } },
  );
  expect(sibling.status()).toBe(403);

  // The traversal download variant must not hand out bytes either.
  const dl = await request.get(
    `/api/netdisk/share/download?token=${encodeURIComponent(shareToken)}&path=${encodeURIComponent("../../../../etc/passwd")}`,
    { headers: { "X-Share-Password": shareCode } },
  );
  expect(dl.status()).not.toBe(200);

  // Missing password on the browsing endpoint: 401 with needsPassword, never a listing.
  const noPass = await request.get(`/api/netdisk/share/public?token=${encodeURIComponent(shareToken)}`);
  expect(noPass.status()).toBe(401);
  expect((await noPass.json()).needsPassword).toBe(true);
});

// ---------------------------------------------------------------------------
// 3. Chunked upload — init validation + honest-client round trip
// ---------------------------------------------------------------------------

test("chunked upload: init rejects degenerate layouts and a consistent layout round-trips", async () => {
  test.setTimeout(60000);

  // Degenerate layouts the server already rejects (chunkupload.go:307-310).
  for (const bad of [
    { path: "", name: "bad.bin", size: -1, chunkSize: 8, totalChunks: 1 },
    { path: "", name: "bad.bin", size: 24, chunkSize: 0, totalChunks: 3 },
    { path: "", name: "bad.bin", size: 24, chunkSize: 8, totalChunks: 0 },
  ]) {
    const r = await admin.post("/api/netdisk/chunk/init", bad);
    expect(r.ok, `init with ${JSON.stringify(bad)} should 400`).toBe(false);
    expect(r.status).toBe(400);
  }

  // Honest layout: 24 bytes as 3x8 chunks (per-chunk CRC omitted — "" disables
  // the optional check, same as the shipped frontend).
  const name = `sec-chunk-${fixture.runId}.bin`;
  const data = Buffer.from("AAAABBBBCCCCDDDDEEEEFFFF");
  const chunkSize = 8;
  const init = await admin.post("/api/netdisk/chunk/init", {
    path: "", name, size: data.length, chunkSize, totalChunks: 3, fileCRC32: "",
  });
  expect(init.ok, init.body).toBe(true);
  const { uploadId } = JSON.parse(init.body);

  for (let i = 0; i < 3; i++) {
    const seg = data.subarray(i * chunkSize, (i + 1) * chunkSize);
    const r = await admin.ctx.post(`/api/netdisk/chunk?path=`, {
      headers: { "X-CSRF-Token": admin.csrf },
      multipart: {
        name,
        uploadId,
        index: String(i),
        hash: "",
        chunk: { name: `${name}.part${i}`, mimeType: "application/octet-stream", buffer: seg },
      },
    });
    expect(r.ok(), `chunk ${i}: ${r.status()} ${await r.text()}`).toBe(true);
  }

  const done = await admin.post("/api/netdisk/chunk/complete", { name, uploadId, fileCRC32: "" });
  expect(done.ok, done.body).toBe(true);

  // The assembled file is byte-identical through the download endpoint.
  const dl = await admin.ctx.get(`/api/netdisk/download?path=${encodeURIComponent(name)}`);
  expect(dl.status()).toBe(200);
  expect(Buffer.from(await dl.body()).equals(data)).toBe(true);
});

// ---------------------------------------------------------------------------
// 4. Logout requires CSRF — verifies the audit L-1 fix end to end
// ---------------------------------------------------------------------------

test("logout without a CSRF token is refused and the session survives (audit L-1 fix)", async ({ request }) => {
  test.setTimeout(60000);
  // A fresh login so this test never disturbs the shared admin client.
  const victim = await apiClient(server.url, server.adminUser, server.adminPassword);
  try {
    const state = await victim.ctx.storageState();
    const session = state.cookies.find((c) => c.name === "mudp_session");
    const csrf = state.cookies.find((c) => c.name === "mudp_csrf");
    expect(session).toBeTruthy();

    // Replay the session cookie WITHOUT the CSRF header — the shape of a
    // cross-site form post riding the ambient cookie. Must be refused.
    const refused = await request.post("/api/logout", {
      headers: { Cookie: `${session.name}=${session.value}` },
      maxRedirects: 0,
    });
    expect(refused.status()).toBe(403);

    // The refused logout left the session intact.
    const still = await request.get("/api/me", {
      headers: { Cookie: `${session.name}=${session.value}` },
    });
    expect(still.status()).toBe(200);
    expect((await still.json()).username).toBe(server.adminUser);

    // With the double-submit pair, logout works as before.
    const done = await request.post("/api/logout", {
      headers: { Cookie: `${session.name}=${session.value}; ${csrf.name}=${csrf.value}`, "X-CSRF-Token": csrf.value },
      maxRedirects: 0,
    });
    expect(done.status()).toBe(200);
  } finally {
    await victim.dispose();
  }
});
