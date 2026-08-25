import { test, expect } from "@playwright/test";
import { startServer } from "./fixtures/server.js";
import { login } from "./fixtures/ui.js";

let server;

// Build the baseURL dynamically from the spawned server.
test.beforeAll(async () => {
  server = await startServer();
  process.env.MUDP_E2E_URL = server.url;
});

test.afterAll(async () => {
  if (server) await server.stop();
});

// Collect every JS error and 5xx response so the test fails if the frontend
// throws or the backend returns an error.
test.beforeEach(({ page }) => {
  page.on("pageerror", (err) => {
    throw new Error(`Page JS error: ${err.message}`);
  });
  page.on("response", (resp) => {
    if (resp.status() >= 500) {
      throw new Error(`Server error ${resp.status()} on ${resp.url()}`);
    }
  });
});

test("login and navigate through core tabs", async ({ page }) => {
  await page.goto("/");

  // Login page renders.
  await expect(page.locator("form.auth-card")).toBeVisible();
  await page.fill("input[name='username']", "admin");
  await page.fill("input[name='password']", "e2e-secret");
  await page.click("form.auth-card .auth-submit");

  // Dashboard loads. refreshAll() may wait for Docker, so give it room.
  await expect(page.locator("aside nav")).toBeVisible({ timeout: 30000 });
  await expect(page.locator("h1")).toBeVisible();

  // Visit each major tab; they should render without JS errors or 5xx.
  for (const tab of ["Containers", "Images", "Volumes", "Networks", "Stacks"]) {
    await page.click(`nav >> text=${tab}`);
    await expect(page.locator(`h1:has-text("${tab}")`)).toBeVisible();
  }
});

test("health and metrics endpoints are reachable", async ({ page, request }) => {
  const health = await request.get("/healthz");
  await expect(health).toBeOK();
  const body = await health.json();
  expect(body.status).toBe("healthy");

  // /metrics exposes process internals, so it requires an admin session.
  await login(page, "admin", "e2e-secret");
  const metrics = await page.request.get("/metrics");
  await expect(metrics).toBeOK();
  const text = await metrics.text();
  expect(text).toContain("mudp_go_goroutines");
});
