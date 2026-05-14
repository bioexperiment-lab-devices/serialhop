import { defineConfig, devices } from "@playwright/test";

const PORT = 4173;

export default defineConfig({
  testDir: "./playwright",
  timeout: 30_000,
  expect: { timeout: 5_000 },
  fullyParallel: true,
  retries: process.env.CI ? 2 : 0,
  reporter: process.env.CI ? [["github"], ["html", { open: "never" }]] : "list",
  use: {
    baseURL: `http://localhost:${PORT}`,
    actionTimeout: 5_000,
  },
  projects: [
    { name: "min",     use: { ...devices["Desktop Chrome"], viewport: { width: 720, height: 480 } } },
    { name: "default", use: { ...devices["Desktop Chrome"], viewport: { width: 980, height: 700 } } },
    { name: "large",   use: { ...devices["Desktop Chrome"], viewport: { width: 1920, height: 1080 } } },
  ],
  webServer: {
    command: `npx vite preview --mode preview --outDir dist-preview --port ${PORT}`,
    url: `http://localhost:${PORT}/preview.html`,
    reuseExistingServer: !process.env.CI,
    timeout: 60_000,
    stdout: "pipe",
    stderr: "pipe",
  },
});
