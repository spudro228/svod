// Обвязка e2e: заливка заметок прямо через API сервера.
//
// Каждый тест заводит себе файлы с уникальными именами, поэтому
// они не мешают друг другу и порядок запуска не важен.

import { expect, type Page } from '@playwright/test'

export const BASE = 'http://127.0.0.1:8123'

/** Тот же токен, что поднимает e2e/serve.sh.
 * Только ASCII: он едет в заголовке Authorization, а тот обязан
 * быть Latin-1 — кириллица роняет fetch ещё до отправки. */
export const TOKEN = 'e2e-test-token-not-for-production'

function auth(): Record<string, string> {
  return { Authorization: `Bearer ${TOKEN}` }
}

function encodePath(path: string): string {
  return path.split('/').map(encodeURIComponent).join('/')
}

/** Кладёт файл в свод так, как это сделал бы демон с диска. */
export async function seed(path: string, content: string, baseHash = ''): Promise<string> {
  // Кириллица в заголовке допустима только в процентном кодировании.
  const headers: Record<string, string> = {
    ...auth(),
    'X-Svod-Device': encodeURIComponent('тест'),
  }
  if (baseHash) headers['If-Match'] = baseHash

  const res = await fetch(`${BASE}/api/v1/files/${encodePath(path)}`, {
    method: 'PUT',
    headers,
    body: content,
  })
  if (!res.ok) throw new Error(`seed ${path}: ${res.status} ${await res.text()}`)
  const body = (await res.json()) as { hash: string }
  return body.hash
}

/** Читает файл с сервера — так проверяем, что правка реально сохранилась. */
export async function fetchContent(path: string): Promise<string> {
  const res = await fetch(`${BASE}/api/v1/raw/${encodePath(path)}`, { headers: auth() })
  if (!res.ok) throw new Error(`raw ${path}: ${res.status}`)
  return res.text()
}

export async function fetchHash(path: string): Promise<string> {
  const res = await fetch(`${BASE}/api/v1/note/${encodePath(path)}`, { headers: auth() })
  if (!res.ok) throw new Error(`note ${path}: ${res.status}`)
  const body = (await res.json()) as { hash: string }
  return body.hash
}

/** Уникальный суффикс, чтобы тесты не спорили за одни и те же пути. */
export function uniq(prefix: string): string {
  return `${prefix}-${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`
}

/** Открывает свод и дожидается, пока дерево наполнится. */
export async function openApp(page: Page): Promise<void> {
  await page.goto('/')
  await expect(page.locator('.panel-l .row').first()).toBeVisible()
}

/** Открывает заметку через ⌘K — самый короткий путь до любого файла. */
export async function openNote(page: Page, path: string): Promise<void> {
  await press(page, 'k')
  const input = page.locator('.palette input')
  await expect(input).toBeVisible()
  await input.fill(path)
  await page.locator('.hit', { hasText: path }).first().click()
  await expect(page.locator('.crumb b')).toContainText(path.split('/').pop()!)
}

/** Нажимает сочетание с Cmd на маке и Ctrl на остальных. */
export async function press(page: Page, key: string, shift = false): Promise<void> {
  const mod = process.platform === 'darwin' ? 'Meta' : 'Control'
  await page.keyboard.press(`${mod}+${shift ? 'Shift+' : ''}${key}`)
}
