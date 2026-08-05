// UI-level regression for stored XSS: server-side escaping (escapeHtml, see
// web/lib/common.js) and Content-Type sandboxing (see internal/server/netdisk.go)
// are already covered by internal/server/security_regression_test.go, but
// those Go tests can only inspect raw HTTP responses — they cannot tell you
// whether the frontend actually renders untrusted data safely in a live DOM,
// or whether a payload silently executes. This spec drives a real browser to
// close that gap: it plants attacker-controlled strings in two different
// surfaces (a group name, and a netdisk filename) and asserts the page never
// executes them and never emits a raw, unescaped element for them.
//
// Same safety model as the rest of the suite — see fixtures/ui.js.

import { test, expect } from "@playwright/test";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { startServer, seed } from "./fixtures/server.js";
import { installPage, login, openTab } from "./fixtures/ui.js";

const PORT = 19107;
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

test("a group name containing a <script> payload renders as inert text, never executes", async ({ page }) => {
  test.setTimeout(60000);
  const h = installPage(page);
  await login(page, server.adminUser, server.adminPassword);
  await openTab(page, "users");

  // A window-level flag the payload would set if it ever actually ran, so a
  // silent escaping bug can't hide behind "no dialog appeared".
  await page.evaluate(() => { window.__xssMarker = false; });

  const payload = `<script>window.__xssMarker=true</script><img src=x onerror="window.__xssMarker=true">`;
  const groupName = `sec-xss-${fixture.runId}-${payload}`;

  await page.fill("#newGroup [name='name']", groupName);
  await page.click("#newGroup button");

  // The new group shows up in all four per-group tables (netdisk/backup/
  // shared-disk paths, language) once the create POST's response triggers a
  // re-render; any one of them proves the point, so just take the first.
  const row = page.locator("table.data tr", { hasText: `sec-xss-${fixture.runId}` }).first();
  await expect(row).toBeVisible({ timeout: 20000 });

  // 1. The payload must be visible as literal text (proves it was escaped
  //    into the DOM as data, not dropped or double-encoded into mush).
  await expect(row).toContainText(payload);

  // 2. No script/img element bearing the payload was actually created
  //    anywhere on the page — i.e. the browser parsed it as text, not markup.
  await expect(page.locator("script", { hasText: "__xssMarker" })).toHaveCount(0);
  await expect(page.locator("img[src='x']")).toHaveCount(0);

  // 3. The payload never actually ran.
  const marker = await page.evaluate(() => window.__xssMarker);
  expect(marker).toBe(false);

  h.assertClean("creating a group with an XSS payload name");
});

test("an uploaded filename with HTML-special characters renders as inert text in the netdisk list", async ({ page }) => {
  test.setTimeout(60000);
  const h = installPage(page);
  await login(page, server.adminUser, server.adminPassword);
  await openTab(page, "netdisk");

  // '<' '>' '"' are illegal in filenames on Windows, so this payload sticks to
  // characters that are valid on every OS the suite runs on while still
  // covering two of the five characters escapeHtml() must handle (& and ').
  const fileName = `xss-${fixture.runId}-'&-probe.txt`;
  const tmpPath = path.join(os.tmpdir(), fileName);
  fs.writeFileSync(tmpPath, `xss probe — ${fixture.runId}\n`);

  await page.evaluate(() => { window.__xssMarker2 = false; });

  try {
    await page.setInputFiles("#uploadFiles", tmpPath);
    const fileRow = page.locator("table.netdisk-table tbody tr", { hasText: `xss-${fixture.runId}` });
    await expect(fileRow).toBeVisible({ timeout: 20000 });
    await expect(fileRow).toContainText(fileName);
  } finally {
    fs.unlinkSync(tmpPath);
  }

  const marker = await page.evaluate(() => window.__xssMarker2);
  expect(marker).toBe(false);

  h.assertClean("uploading a filename with HTML-special characters");
});
