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

  globalSetup: './e2e/global-setup.ts',

  use: {
    baseURL: BASE,
    trace: 'retain-on-failure',
    // Вход выполнен один раз в global-setup; гостевые сценарии заводят
    // свой контекст и этой куки не наследуют.
    storageState: 'e2e/.auth.json',
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
