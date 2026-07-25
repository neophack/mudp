// First-run experience: before any administrator account exists, the app must
// show the setup wizard instead of the login form, refuse invalid input, and
// hand off cleanly to a normal login once setup completes.
//
// Same safety model as the other specs — see fixtures/ui.js.

import { test, expect } from "@playwright/test";
import { startServer } from "./fixtures/server.js";
import { installPage } from "./fixtures/ui.js";

const PORT = 19102;
test.use({ baseURL: `http://127.0.0.1:${PORT}` });

test.describe.configure({ mode: "serial" });

let server;

// adminPassword: "" is the signal fixtures/server.js uses to skip bootstrapping
// an admin account, leaving the server in its genuine first-run state.
test.beforeEach(async () => {
  server = await startServer({ port: PORT, adminPassword: "" });
});

test.afterEach(async () => {
  if (server) await server.stop();
});

test("first run shows the setup wizard instead of the login form", async ({ page }) => {
  const h = installPage(page);
  await page.goto("/");
  await expect(page.locator("#setupForm")).toBeVisible();
  await expect(page.locator("#loginForm")).toHaveCount(0);
  h.assertClean("the first-run redirect");
});

test("the wizard surfaces a backend validation error and stays put", async ({ page }) => {
  const h = installPage(page);
  await page.goto("/");
  // Whitespace satisfies the input's "required" attribute client-side but is
  // trimmed to empty server-side, so this exercises the backend's own check
  // (and the error-toast rendering path) rather than just HTML5 validation.
  await page.fill("input[name='adminUsername']", "   ");
  await page.fill("input[name='adminPassword']", "some-password");
  await page.click("#setupForm button.primary");

  await expect(page.locator(".setup-error")).toBeVisible();
  await expect(page.locator("#setupForm")).toBeVisible();
  await expect(page.locator("aside nav")).toHaveCount(0);
  h.assertClean("the whitespace-username setup submission");
});

test("completing setup creates the admin account and it can log in afterwards", async ({ page }) => {
  const h = installPage(page);
  await page.goto("/");
  await expect(page.locator("#setupForm")).toBeVisible();

  const username = "setupadmin";
  const password = "setup-secret-1";
  await page.fill("input[name='adminUsername']", username);
  await page.fill("input[name='adminPassword']", password);
  await page.fill("input[name='siteName']", "E2E Setup Site");
  await page.click("#setupForm button.primary");

  // Setup hands off to the login form.
  await expect(page.locator("#loginForm")).toBeVisible({ timeout: 15000 });

  // The wizard cannot run twice: a fresh load now goes straight to login.
  await page.reload();
  await expect(page.locator("#loginForm")).toBeVisible();
  await expect(page.locator("#setupForm")).toHaveCount(0);

  // And the account it created actually works.
  await page.fill("input[name='username']", username);
  await page.fill("input[name='password']", password);
  await page.click("#loginForm button.primary");
  await expect(page.locator("aside nav")).toBeVisible({ timeout: 45000 });
  await expect(page.locator("aside .profile")).toContainText(username);

  h.assertClean("the completed setup wizard");
});
