import { test, expect } from "@playwright/test";
import { startServer } from "./fixtures/server.js";
import { login } from "./fixtures/ui.js";

let server;

test.beforeAll(async () => {
  server = await startServer();
  process.env.MUDP_E2E_URL = server.url;
});

test.afterAll(async () => {
  if (server) await server.stop();
});

// Regression: sidebarCollapsed must be a reactive store key declared upfront.
// When it was added to the store after creation, Vue 2 never saw the toggle
// mutate it, so clicking expand/collapse did nothing.
test("sidebar collapse toggle collapses, persists across reload, and expands", async ({ page }) => {
  await login(page, server.adminUser, server.adminPassword);

  const aside = page.locator("aside.shell-aside");
  const toggle = page.locator(".sidebar-toggle");
  await expect(aside).not.toHaveClass(/collapsed/);
  await expect(aside.locator(".nav-label").first()).toBeVisible();

  await toggle.click();
  await expect(aside).toHaveClass(/collapsed/);
  await expect(aside.locator(".nav-label").first()).toBeHidden();

  // The collapsed state survives a reload (localStorage-backed).
  await page.reload();
  await expect(page.locator("aside nav")).toBeVisible();
  await expect(page.locator("aside.shell-aside")).toHaveClass(/collapsed/);

  await page.locator(".sidebar-toggle").click();
  await expect(page.locator("aside.shell-aside")).not.toHaveClass(/collapsed/);
  await expect(page.locator("aside .nav-label").first()).toBeVisible();
});
