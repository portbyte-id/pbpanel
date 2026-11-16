import { defineConfig, devices } from "@playwright/test"

/**
 * One journey, against a real panel.
 *
 * `webServer` runs `scripts/e2e/server.go`, which boots the actual daemon
 * (SQLite, settings registry, auth, embedded SPA) over a self-signed certificate
 * on loopback. That is deliberate: a mocked API would prove the components
 * render, not that login, the CSRF header and the settings write actually work
 * against the server this SPA ships with.
 */
const PORT = 18443
const BASE_URL = `https://127.0.0.1:${PORT}`
const PASSWORD = "e2e-password-123"

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [["list"]],
  use: {
    baseURL: BASE_URL,
    // The panel's certificate is generated at install time and is self-signed,
    // so the browser must be told to trust this origin for the run.
    ignoreHTTPSErrors: true,
    trace: "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  webServer: {
    command: `go run ../scripts/e2e/server.go -addr 127.0.0.1:${PORT} -password ${PASSWORD}`,
    url: BASE_URL,
    ignoreHTTPSErrors: true,
    reuseExistingServer: false,
    timeout: 120_000,
    stdout: "pipe",
    stderr: "pipe",
  },
})
