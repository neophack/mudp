// One-off GUI check for the stacks feature toggle: a fresh instance ships
// with compose stacks closed for regular users; the admin flips it on in
// Settings and the user's sidebar picks it up. Run:
//   node tests/e2e/stacks-toggle-check.mjs
import { chromium } from "playwright";
import { startServer } from "./fixtures/server.js";

const browser = await chromium.launch();
const errors = [];
let server;

function watch(page) {
  page.on("pageerror", (e) => errors.push(`${page.url()}: ${e.message}`));
  page.on("console", (m) => {
    if (m.type() === "error" && !m.text().startsWith("Failed to load resource")) errors.push(`${page.url()}: console ${m.text()}`);
  });
}

async function login(page, username, password) {
  await page.goto(server.url);
  await page.waitForSelector("form.auth-card");
  await page.fill("input[name='username']", username);
  await page.fill("input[name='password']", password);
  const [capResp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes("/api/captcha")),
    page.click(".captcha-img"),
  ]);
  await page.fill("input[name='captcha']", capResp.headers()["x-mudp-captcha-answer"]);
  await page.click("form.auth-card .auth-submit");
  await page.waitForSelector("aside nav", { timeout: 60000 });
}

function fail(msg) {
  console.error(`FAIL: ${msg}`);
  process.exitCode = 1;
}

try {
  server = await startServer({ port: 19433 });
  const admin = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  watch(admin);
  await login(admin, "admin", "e2e-secret");

  // Create a regular user through the API with the admin's session.
  const created = await admin.evaluate(async () => {
    const res = await fetch("/api/users", {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": decodeURIComponent(document.cookie.match(/(?:^|; )mudp_csrf=([^;]+)/)?.[1] || "") },
      body: JSON.stringify({ username: "alice", password: "Alice-Pass-2026!", role: "user", groupId: 0, containerCap: 3, netdiskQuotaBytes: 0 }),
    });
    return res.status;
  });
  if (created !== 200) fail(`user create returned ${created}`);

  // Default state: admin sees Stacks, the user does not, and the route bounces.
  if (!(await admin.locator("nav button[data-tab='stacks']").count())) fail("admin lost the stacks tab");
  const stacksApi = await admin.evaluate(async () => (await fetch("/api/admin/settings/stacks")).json());
  if (stacksApi.enabled !== false) fail(`fresh instance reports stacks enabled=${stacksApi.enabled}, want false`);

  const user = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  watch(user);
  await login(user, "alice", "Alice-Pass-2026!");
  if (await user.locator("nav button[data-tab='stacks']").count()) fail("user sees the stacks tab while the feature is off");
  await user.goto(`${server.url}/stacks`);
  await user.waitForURL(/dashboard/);
  if (!user.url().includes("/dashboard")) fail(`direct /stacks visit landed on ${user.url()}, want redirect to dashboard`);

  // Admin flips the toggle in Settings and it survives a reload.
  await admin.click("nav button[data-tab='settings']");
  const row = admin.locator(".row", { hasText: "堆栈功能" });
  await row.locator(".el-switch").click();
  await admin.waitForResponse((r) => r.url().includes("/api/admin/settings/stacks") && r.request().method() === "POST");
  await admin.reload();
  await admin.waitForSelector("nav");
  await admin.click("nav button[data-tab='settings']");
  if (!(await admin.locator(".row", { hasText: "堆栈功能" }).locator(".el-switch.is-checked").count())) {
    fail("stacks toggle did not stay checked after reload");
  }

  // The user's session now shows the tab and can open the page.
  await user.reload();
  await user.waitForSelector("nav");
  if (!(await user.locator("nav button[data-tab='stacks']").count())) fail("user still cannot see the stacks tab after enable");
  await user.click("nav button[data-tab='stacks']");
  await user.waitForURL(/stacks/);
  await user.waitForSelector("h1");

  // And off again: the tab disappears on the next reload.
  await admin.locator(".row", { hasText: "堆栈功能" }).locator(".el-switch").click();
  await user.waitForTimeout(500);
  await user.reload();
  await user.waitForSelector("nav");
  if (await user.locator("nav button[data-tab='stacks']").count()) fail("user still sees the stacks tab after re-disable");

  if (errors.length) fail(`page errors: ${errors.join(" | ")}`);
  if (!process.exitCode) console.log("OK: stacks toggle behaves end to end");
} finally {
  await browser.close();
  if (server) await server.stop();
}
