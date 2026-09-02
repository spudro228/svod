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
}

export type SearchHit = { path: string; title: string; snippet: string }

export type Health = { ok: boolean; seq: number; fts: boolean }

/** Кодируем посегментно: разделители остаются разделителями. */
function encodePath(path: string): string {
  return path.split('/').map(encodeURIComponent).join('/')
}

async function get<T>(url: string): Promise<T> {
  const res = await fetch(url, { headers: { Accept: 'application/json' } })
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
}

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
