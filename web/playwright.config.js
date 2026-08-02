import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./tests/e2e",
  // Generous headroom above the default 30s: beforeAll hooks boot a real mudp
  // binary and seed fixture data (containers, volumes, users, ...), which can
  // take 20-25s on its own and tips over the default under full-suite load.
  timeout: 60000,
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: 1,
  reporter: "list",
  use: {
    baseURL: process.env.MUDP_E2E_URL || "http://127.0.0.1:19000",
    trace: "on-first-retry",
  },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },
  ],
});
