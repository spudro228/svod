// Один вход на весь прогон: дальше тесты работают с готовой кукой.
// Гостевые сценарии заводят свой чистый контекст и этой куки не видят.

import { chromium, type FullConfig } from '@playwright/test'
import { BASE, TOKEN } from './svod'

export default async function globalSetup(_config: FullConfig) {
  const browser = await chromium.launch()
  const ctx = await browser.newContext({ baseURL: BASE })

  const res = await ctx.request.post('/api/v1/auth', { data: { token: TOKEN } })
  if (!res.ok()) throw new Error(`вход не удался: ${res.status()}`)

  await ctx.storageState({ path: 'e2e/.auth.json' })
  await browser.close()
}
