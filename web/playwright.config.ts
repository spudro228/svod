import { defineConfig, devices } from '@playwright/test'

const BASE = 'http://127.0.0.1:8123'

export default defineConfig({
  testDir: './e2e',
  timeout: 20_000,
  expect: { timeout: 7_000 },
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [['list']],

  use: {
    baseURL: BASE,
    trace: 'retain-on-failure',
  },

  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],

  // Настоящий svodd с вшитым фронтом: тестируем то, что уедет на VPS.
  webServer: {
    command: 'sh e2e/serve.sh',
    url: `${BASE}/healthz`,
    reuseExistingServer: false,
    timeout: 120_000,
    stdout: 'pipe',
    stderr: 'pipe',
  },
})
