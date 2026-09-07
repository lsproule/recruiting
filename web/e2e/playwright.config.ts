import { defineConfig } from "@playwright/test";

// The processes and the fixture data come from run.sh, which boots serve,
// worker, and runner against the Compose stack before Playwright starts.
const baseURL = process.env.E2E_BASE_URL ?? "http://localhost:8090";

export default defineConfig({
  testDir: "./tests",
  globalSetup: "./global-setup.ts",
  // The scenarios share one org, one job, and one candidate sitting, so they
  // run one at a time rather than racing each other through it.
  workers: 1,
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  reporter: [["list"]],
  timeout: 120_000,
  expect: { timeout: 15_000 },
  use: {
    baseURL,
    headless: true,
    trace: "retain-on-failure",
    // No camera exists on the host, so Chromium synthesises one: a rolling
    // test pattern for getUserMedia and an automatic grant of the prompt.
    launchOptions: {
      args: [
        "--use-fake-device-for-media-stream",
        "--use-fake-ui-for-media-stream",
      ],
    },
  },
  projects: [
    {
      name: "chromium",
      use: {
        browserName: "chromium",
        permissions: ["camera"],
        viewport: { width: 1400, height: 1000 },
      },
    },
  ],
});
