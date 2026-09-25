// Public pages: login (errors + language switch), the first-run setup wizard
// end to end, and the pending-approval interstitial.
import { test, expect } from "@playwright/test";
import { startServer, seed } from "./fixtures/server.js";
import { installPage, login, loginCaptured, fillCaptcha, logout, openTab } from "./fixtures/ui.js";

test.use({ baseURL: "http://127.0.0.1:19043" });

let server;

test.beforeAll(async () => {
  server = await startServer({ port: 19043 });
  await seed(server, { runId: "auth" });
});

test.afterAll(async () => {
  if (server) await server.stop();
});

let helper;

test.beforeEach(async ({ page }) => {
  // Playwright's default locale is en-US; pin the app to Chinese so the
  // zh UI copy the assertions rely on is actually rendered.
  await page.addInitScript(() => { if (!localStorage.getItem("mudp_language")) localStorage.setItem("mudp_language", "zh_CN"); });
  helper = installPage(page);
});

test.afterEach(async ({ page }) => {
  helper.assertClean("", { ignore503: true });
});

test("login: wrong captcha and wrong password surface localized errors", async ({ page }) => {
  await page.goto("/");
  await page.locator("form.auth-card").waitFor();
  await page.fill("input[name='username']", "admin");
  await page.fill("input[name='password']", "wrong-pass-123");
  await page.fill("input[name='captcha']", "XXXX");
  await page.click("form.auth-card .auth-submit");
  await expect(page.locator(".el-message").last()).toBeVisible();
  await expect(page.locator("form.auth-card")).toBeVisible();
});

test("login: language switch to English persists across reload", async ({ page }) => {
  await page.goto("/");
  await page.locator("form.auth-card").waitFor();
  await expect(page.locator("form.auth-card h1")).toContainText("登录");

  await page.locator(".lang-btn", { hasText: "English" }).click();
  await expect(page.locator("form.auth-card h1")).toContainText("Sign in");

  await page.reload();
  await page.locator("form.auth-card").waitFor();
  await expect(page.locator("form.auth-card h1")).toContainText("Sign in");

  // switch back so later tests see the Chinese UI
  await page.locator(".lang-btn", { hasText: "中文" }).click();
  await expect(page.locator("form.auth-card h1")).toContainText("登录");
});

test("login: correct credentials reach the dashboard", async ({ page }) => {
  await login(page, server.adminUser, server.adminPassword);
  await expect(page).toHaveURL(/dashboard/);
});

test("pending: a user in the pending group lands on the approval page", async ({ page }) => {
  // admin moves the seeded user into the pending group via the Users page
  await login(page, server.adminUser, server.adminPassword);
  await openTab(page, "users");
  await page.locator(".el-table__row", { hasText: "e2euser" }).first().locator("button", { hasText: "用户组" }).first().click();
  const dlg = page.locator(".el-dialog:visible").first();
  await dlg.waitFor();
  await dlg.getByText("pending").first().click();
  await dlg.locator("button", { hasText: "保存" }).last().click();
  await page.waitForTimeout(600);
  await logout(page);

  // the user now signs in and is held at /pending
  await page.goto("/");
  await page.locator("form.auth-card").waitFor();
  await page.fill("input[name='username']", "e2euser");
  await page.fill("input[name='password']", "e2e-user-secret");
  await fillCaptcha(page);
  await page.click("form.auth-card .auth-submit");
  await expect(page).toHaveURL(/pending/, { timeout: 15000 });
  await expect(page.locator(".pending-card")).toContainText("等待管理员审批");
  // the greeting shows the name, not raw markup
  await expect(page.locator(".pending-card")).toContainText("e2euser");
  await expect(page.locator(".pending-card")).not.toContainText("<strong>");

  // restore: admin puts the user back into users
  await page.locator(".pending-card button", { hasText: "登出" }).click();
  await expect(page.locator("form.auth-card")).toBeVisible({ timeout: 10000 });
  await login(page, server.adminUser, server.adminPassword);
  await openTab(page, "users");
  await page.locator(".el-table__row", { hasText: "e2euser" }).first().locator("button", { hasText: "用户组" }).first().click();
  const dlg2 = page.locator(".el-dialog:visible").first();
  await dlg2.waitFor();
  await dlg2.getByText("users", { exact: true }).first().click();
  await dlg2.locator("button", { hasText: "保存" }).last().click();
});

test.describe("first-run setup wizard", () => {
  // The wizard runs on its own server; repoint the relative-URL helpers.
  test.use({ baseURL: "http://127.0.0.1:19045" });

  let setupServer;

  test.beforeAll(async () => {
    setupServer = await startServer({ port: 19045, adminPassword: "" });
  });
  test.afterAll(async () => {
    if (setupServer) await setupServer.stop();
  });

  test("completes the wizard and lands on the dashboard", async ({ page }) => {
    // Playwright's default locale is en-US; pin the app to Chinese so the
  // zh UI copy the assertions rely on is actually rendered.
  await page.addInitScript(() => { if (!localStorage.getItem("mudp_language")) localStorage.setItem("mudp_language", "zh_CN"); });
  helper = installPage(page);
    await page.goto(`http://127.0.0.1:19045/`);
    await page.locator("form.auth-card").waitFor();
    await expect(page.locator("form.auth-card h1")).toContainText("初始");

    await page.fill("input[name='adminUsername']", "wizard-admin");
    await page.fill("input[name='adminPassword']", "wizard-password-1");
    await page.fill("input[name='siteName']", "向导站点");
    // Wait for the init POST to land: navigating earlier aborts it mid-flight
    // and the reload would show the wizard again with setup still pending.
    const [initResp] = await Promise.all([
      page.waitForResponse((r) => r.url().includes("/api/setup/init"), { timeout: 15000 }),
      page.click("form.auth-card .auth-submit"),
    ]);
    if (!initResp.ok()) throw new Error(`setup init failed: ${initResp.status()}`);

    // the wizard hands over to the login page; the fresh credentials work
    await expect(page.locator("form.auth-card")).toBeVisible({ timeout: 15000 });
    await loginCaptured(page, "wizard-admin", "wizard-password-1");
    await expect(page).toHaveURL(/dashboard/);
    await expect(page.locator(".shell-aside .brand-text")).toContainText("向导站点");
  });
});
