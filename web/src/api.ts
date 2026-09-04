// Клиент API Свода. Типы повторяют internal/proto.

export type FileMeta = {
  path: string
  hash: string
  size: number
  mtime: number
  seq: number
  deleted?: boolean
  title?: string
}

export type Heading = { level: number; text: string; id: string }

export type Note = {
  path: string
  hash: string
  seq: number
  size: number
  mtime: number
  title: string
  content: string
  headings: Heading[]
  tags: string[]
  links: string[]
  backlinks: string[]
  aliases?: string[]
  meta?: Record<string, string>
  binary?: boolean
}

export type SearchHit = { path: string; title: string; snippet: string }

export type Share = {
  key: string
  path: string
  created: number
  expires: number
  url: string
}

export type Version = {
  seq: number
  hash: string
  deleted?: boolean
  at: number
  device: string
  /** Заполнен, только если версия относится к прежнему имени файла. */
  path?: string
}

/** Сервер не пустил: нужен вход. */
export class UnauthorizedError extends Error {
  constructor() {
    super('нужен токен доступа')
    this.name = 'UnauthorizedError'
  }
}

/** Сервер отказался писать: там уже другая версия. */
export class ConflictError extends Error {
  constructor(
    readonly serverHash: string,
    readonly seq: number,
  ) {
    super('на сервере другая версия')
    this.name = 'ConflictError'
  }
}

export type Health = { ok: boolean; seq: number; fts: boolean }

/** Кодируем посегментно: разделители остаются разделителями. */
export function encodePath(path: string): string {
  return path.split('/').map(encodeURIComponent).join('/')
}

/** Адрес файла для показа в странице: картинки, pdf и прочие вложения. */
export function rawURL(path: string): string {
  return `/api/v1/raw/${encodePath(path)}`
}

async function get<T>(url: string): Promise<T> {
  const res = await fetch(url, { headers: { Accept: 'application/json' } })
  if (res.status === 401) throw new UnauthorizedError()
  if (!res.ok) {
    let msg = `${res.status} ${res.statusText}`
    try {
      const body = (await res.json()) as { error?: string }
      if (body.error) msg = body.error
    } catch {
      // тело не JSON — оставляем код ответа
    }
    throw new Error(msg)
  }
  return (await res.json()) as T
}

export const api = {
  health: () => get<Health>('/healthz'),
  tree: () => get<{ files: FileMeta[]; seq: number }>('/api/v1/tree'),
  note: (path: string) => get<Note>(`/api/v1/note/${encodePath(path)}`),
  search: (q: string) =>
    get<{ hits: SearchHit[] }>(`/api/v1/search?q=${encodeURIComponent(q)}&limit=50`),
  /** Нужен ли вход и выполнен ли он. */
  authState: () => get<{ required: boolean; authorized: boolean }>('/api/v1/auth'),

  /** Меняет токен на сессионную куку — дальше браузер шлёт её сам. */
  async login(token: string): Promise<void> {
    const res = await fetch('/api/v1/auth', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ token: token.trim() }),
    })
    if (res.status === 401) throw new Error('Токен не подошёл')
    if (!res.ok) throw new Error(`Сервер ответил ${res.status}`)
  },

  async logout(): Promise<void> {
    await fetch('/api/v1/logout', { method: 'POST' })
  },

  history: (path: string) =>
    get<{ versions: Version[] }>(`/api/v1/history/${encodePath(path)}`),
  tags: () => get<{ tags: Record<string, number> }>('/api/v1/tags'),

  /** Порядок корневых папок живёт на сервере: он переезжает между устройствами. */
  order: () => get<{ order: string[] }>('/api/v1/order'),

  async setOrder(order: string[]): Promise<string[]> {
    const res = await fetch('/api/v1/order', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ order }),
    })
    if (res.status === 401) throw new UnauthorizedError()
    if (!res.ok) throw new Error(`Не смог сохранить порядок: ${res.status}`)
    return ((await res.json()) as { order: string[] }).order
  },

  /** Временные ссылки: выдать, перечислить, отозвать. */
  shares: () => get<{ shares: Share[] }>('/api/v1/share'),

  async share(path: string, hours: number): Promise<Share> {
    const res = await fetch('/api/v1/share', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ path, hours }),
    })
    if (res.status === 401) throw new UnauthorizedError()
    if (!res.ok) throw new Error(`Не смог выдать ссылку: ${res.status}`)
    return (await res.json()) as Share
  },

  async revokeShare(key: string): Promise<void> {
    const res = await fetch(`/api/v1/share/${encodeURIComponent(key)}`, { method: 'DELETE' })
    if (!res.ok) throw new Error(`Не смог отозвать: ${res.status}`)
  },
  byTag: (tag: string) =>
    get<{ paths: string[] }>(`/api/v1/tags?tag=${encodeURIComponent(tag)}`),

  /** Содержимое конкретной версии — блобы неизменяемы. */
  async blob(hash: string): Promise<string> {
    const res = await fetch(`/api/v1/blob/${hash}`)
    if (!res.ok) throw new Error(`версия не найдена: ${res.status}`)
    return res.text()
  },

  /**
   * Сохранить файл. baseHash — версия, от которой отталкивались;
   * пустая строка означает «файла ещё нет».
   * Несовпадение поднимает ConflictError: сервер ничего не перезаписал.
   */
  async save(path: string, content: BodyInit, baseHash: string): Promise<PutResult> {
    // Заголовки HTTP только Latin-1: кириллица в значении роняет fetch
    // с TypeError ещё до отправки. Сервер разбирает кодирование обратно.
    const headers: Record<string, string> = {
      'X-Svod-Device': encodeURIComponent('браузер'),
    }
    if (baseHash) headers['If-Match'] = baseHash

    const res = await fetch(`/api/v1/files/${encodePath(path)}`, {
      method: 'PUT',
      headers,
      body: content,
    })
    if (res.status === 401) throw new UnauthorizedError()
    if (res.status === 409) {
      const body = (await res.json()) as { server_hash: string; seq: number }
      throw new ConflictError(body.server_hash, body.seq)
    }
    if (!res.ok) {
      let msg = `${res.status} ${res.statusText}`
      try {
        const body = (await res.json()) as { error?: string }
        if (body.error) msg = body.error
      } catch {
        // тело не JSON
      }
      throw new Error(msg)
    }
    return (await res.json()) as PutResult
  },
}

export type PutResult = { seq: number; hash: string }

/**
 * Подписка на поток изменений. Сервер шлёт только номер seq —
 * это повод перечитать дерево, а не само содержимое.
 */
export function openStream(onEvent: (seq: number, path: string) => void, onState: (up: boolean) => void) {
  let ws: WebSocket | null = null
  let retry: number | undefined
  let closed = false

  const connect = () => {
    if (closed) return
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    ws = new WebSocket(`${proto}//${location.host}/api/v1/stream`)

    ws.onopen = () => onState(true)
    ws.onmessage = (e) => {
      try {
        const ev = JSON.parse(e.data as string) as { seq: number; path: string }
        onEvent(ev.seq, ev.path)
      } catch {
        // мусор в кадре — игнорируем, следующий seq всё равно догонит
      }
    }
    ws.onclose = () => {
      onState(false)
      if (!closed) retry = window.setTimeout(connect, 2000)
    }
    ws.onerror = () => ws?.close()
  }

  connect()
  return () => {
    closed = true
    window.clearTimeout(retry)
    ws?.close()
  }
}
