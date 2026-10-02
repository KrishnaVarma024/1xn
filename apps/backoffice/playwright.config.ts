import { defineConfig, devices } from "@playwright/test";

// End-to-end tests drive the real back office in a real browser against a
// real Go API and Postgres. Run them with `make test-web` from the repo root:
// it starts a throwaway database and API, and points E2E_API_URL at it.
const api = process.env.E2E_API_URL ?? "http://localhost:8080";
const port = Number(process.env.E2E_WEB_PORT ?? 3100);

export default defineConfig({
  testDir: "./e2e",
  timeout: 30_000,
  fullyParallel: false,
  retries: 0,
  reporter: [["list"]],
  use: {
    baseURL: `http://localhost:${port}`,
    trace: "retain-on-failure",
    locale: "en-IN",
    timezoneId: "Asia/Kolkata",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  webServer: {
    command: `npx next start --port ${port}`,
    url: `http://localhost:${port}/sign-in`,
    reuseExistingServer: false,
    env: { ONEXN_API_URL: api, NEXT_TELEMETRY_DISABLED: "1" },
  },
});
