import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api, openStream, type FileMeta, type Note, type SearchHit } from './api'
import { headingId, renderNote } from './md'

// ───────────────────────── дерево ─────────────────────────

type TreeNode = {
  name: string
  path: string
  dir: boolean
  children: TreeNode[]
}

function buildTree(files: FileMeta[]): TreeNode[] {
  const root: TreeNode = { name: '', path: '', dir: true, children: [] }

  for (const f of files) {
    const parts = f.path.split('/')
    let cur = root
    parts.forEach((part, i) => {
      const isLeaf = i === parts.length - 1
      const path = parts.slice(0, i + 1).join('/')
      let next = cur.children.find((c) => c.name === part && c.dir === !isLeaf)
      if (!next) {
        next = { name: part, path, dir: !isLeaf, children: [] }
        cur.children.push(next)
      }
      cur = next
    })
  }

  const sortRec = (n: TreeNode) => {
    n.children.sort((a, b) =>
      a.dir === b.dir ? a.name.localeCompare(b.name, 'ru') : a.dir ? -1 : 1,
    )
    n.children.forEach(sortRec)
  }
  sortRec(root)
  return root.children
}

/** Ищем цель wiki-ссылки: сначала по полному пути, потом по имени файла. */
function resolveLink(files: FileMeta[], target: string): string | null {
  const clean = target.replace(/\.md$/i, '')
  const exact = files.find((f) => f.path.replace(/\.md$/i, '') === clean)
  if (exact) return exact.path

  const base = clean.split('/').pop()?.toLowerCase() ?? ''
  const byName = files.find(
    (f) => (f.path.split('/').pop() ?? '').replace(/\.md$/i, '').toLowerCase() === base,
  )
  return byName ? byName.path : null
}

function displayName(path: string): string {
  return (path.split('/').pop() ?? path).replace(/\.md$/i, '')
}

// ───────────────────────── приложение ─────────────────────────

type Overlay = null | 'files' | 'search'
type Mode = 'read' | 'source'

export default function App() {
  const [files, setFiles] = useState<FileMeta[]>([])
  const [seq, setSeq] = useState(0)
  const [online, setOnline] = useState(false)
  const [syncedAt, setSyncedAt] = useState<number>(Date.now())
  const [error, setError] = useState<string | null>(null)

  const [path, setPath] = useState<string | null>(null)
  const [note, setNote] = useState<Note | null>(null)
  const [mode, setMode] = useState<Mode>('read')

  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [showLeft, setShowLeft] = useState(true)
  const [showRight, setShowRight] = useState(true)
  const [overlay, setOverlay] = useState<Overlay>(null)

  const tree = useMemo(() => buildTree(files), [files])

  const loadTree = useCallback(async () => {
    try {
      const t = await api.tree()
      setFiles(t.files)
      setSeq(t.seq)
      setError(null)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [])

  const open = useCallback(async (p: string) => {
    setPath(p)
    setOverlay(null)
    try {
      setNote(await api.note(p))
      setError(null)
    } catch (e) {
      setNote(null)
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [])

  // Первая загрузка и живой поток изменений.
  useEffect(() => {
    void loadTree()
    const close = openStream(
      (newSeq) => {
        setSeq(newSeq)
        setSyncedAt(Date.now())
        void loadTree()
      },
      (up) => setOnline(up),
    )
    return close
  }, [loadTree])

  // Открытая заметка обновляется, когда её версия уехала вперёд.
  useEffect(() => {
    if (!path) return
    const meta = files.find((f) => f.path === path)
    if (meta && note && meta.hash !== note.hash) void open(path)
  }, [files, path, note, open])

  // Раскрываем ветки до выбранного файла.
  useEffect(() => {
    if (!path) return
    setExpanded((prev) => {
      const next = new Set(prev)
      const parts = path.split('/')
      for (let i = 1; i < parts.length; i++) next.add(parts.slice(0, i).join('/'))
      return next
    })
  }, [path])

  // Горячие клавиши из спеки.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const meta = e.metaKey || e.ctrlKey
      if (e.key === 'Escape') {
        setOverlay(null)
        return
      }
      if (!meta) return

      const k = e.key.toLowerCase()
      if (k === 'k') {
        e.preventDefault()
        setOverlay('files')
      } else if (k === 'f' && e.shiftKey) {
        e.preventDefault()
        setOverlay('search')
      } else if (k === 'e') {
        e.preventDefault()
        setMode((m) => (m === 'read' ? 'source' : 'read'))
      } else if (k === '\\') {
        e.preventDefault()
        setShowLeft((v) => !v)
      } else if (k === '.') {
        e.preventDefault()
        setShowRight((v) => !v)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  const bodyClass = ['body', showLeft ? '' : 'no-left', showRight ? '' : 'no-right']
    .filter(Boolean)
    .join(' ')

  return (
    <div className="app">
      <TopBar path={path} mode={mode} onMode={setMode} />

      <div className={bodyClass}>
        <nav className="panel panel-l">
          <p className="label">Свод</p>
          {tree.length === 0 && <p className="label">пусто</p>}
          <Tree
            nodes={tree}
            depth={0}
            active={path}
            expanded={expanded}
            onToggle={(p) =>
              setExpanded((prev) => {
                const next = new Set(prev)
                if (next.has(p)) next.delete(p)
                else next.add(p)
                return next
              })
            }
            onOpen={open}
          />
        </nav>

        <main className="main">
          {note ? (
            <NoteView note={note} mode={mode} files={files} onOpen={open} />
          ) : (
            <Welcome error={error} count={files.length} />
          )}
        </main>

        <aside className="panel panel-r">
          <SidePanel note={note} onOpen={open} />
        </aside>
      </div>

      <StatusBar
        online={online}
        seq={seq}
        count={files.length}
        syncedAt={syncedAt}
        error={error}
      />

      {overlay && (
        <Palette
          kind={overlay}
          files={files}
          onClose={() => setOverlay(null)}
          onOpen={open}
        />
      )}
    </div>
  )
}

// ───────────────────────── шапка ─────────────────────────

function TopBar({
  path,
  mode,
  onMode,
}: {
  path: string | null
  mode: Mode
  onMode: (m: Mode) => void
}) {
  const parts = path ? path.split('/') : []
  const file = parts.pop()

  return (
    <header className="top">
      <span className="crumb">
        {path ? (
          <>
            {parts.map((p) => `${p} / `)}
            <b>{file}</b>
          </>
        ) : (
          'Свод'
        )}
      </span>
      <span className="top-right">
        <button
          className={`pill ${mode === 'source' ? 'is-on' : ''}`}
          onClick={() => onMode(mode === 'read' ? 'source' : 'read')}
        >
          {mode === 'read' ? 'ЧТЕНИЕ' : 'ИСХОДНИК'}
        </button>
        <span className="kbd">⌘E</span>
      </span>
    </header>
  )
}

// ───────────────────────── дерево ─────────────────────────

function Tree({
  nodes,
  depth,
  active,
  expanded,
  onToggle,
  onOpen,
}: {
  nodes: TreeNode[]
  depth: number
  active: string | null
  expanded: Set<string>
  onToggle: (p: string) => void
  onOpen: (p: string) => void
}) {
  return (
    <>
      {nodes.map((n) => {
        const isOpen = expanded.has(n.path)
        return (
          <div key={n.path}>
            <button
              className={`row ${!n.dir && n.path === active ? 'is-active' : ''}`}
              style={{ paddingLeft: 12 + depth * 12 }}
              onClick={() => (n.dir ? onToggle(n.path) : onOpen(n.path))}
              title={n.path}
            >
              <span className="caret">{n.dir ? (isOpen ? '▾' : '▸') : ''}</span>
              <span className="name">{n.dir ? n.name : displayName(n.name)}</span>
            </button>
            {n.dir && isOpen && (
              <Tree
                nodes={n.children}
                depth={depth + 1}
                active={active}
                expanded={expanded}
                onToggle={onToggle}
                onOpen={onOpen}
              />
            )}
          </div>
        )
      })}
    </>
  )
}

// ───────────────────────── заметка ─────────────────────────

function NoteView({
  note,
  mode,
  files,
  onOpen,
}: {
  note: Note
  mode: Mode
  files: FileMeta[]
  onOpen: (p: string) => void
}) {
  const ref = useRef<HTMLDivElement>(null)
  const html = useMemo(() => renderNote(note.content), [note.content])

  // Проставляем якоря заголовкам и ловим клики по wiki-ссылкам.
  useEffect(() => {
    const el = ref.current
    if (!el || mode !== 'read') return

    const seen = new Map<string, number>()
    el.querySelectorAll('h1, h2, h3, h4, h5, h6').forEach((h) => {
      const base = headingId(h.textContent ?? '')
      const n = seen.get(base) ?? 0
      seen.set(base, n + 1)
      h.id = n === 0 ? base : `${base}-${n}`
    })

    const onClick = (e: MouseEvent) => {
      const link = (e.target as HTMLElement).closest('a.wikilink')
      if (!link) return
      e.preventDefault()
      const target = link.getAttribute('data-target') ?? ''
      const resolved = resolveLink(files, target)
      if (resolved) onOpen(resolved)
    }
    el.addEventListener('click', onClick)
    return () => el.removeEventListener('click', onClick)
  }, [html, mode, files, onOpen])

  const date = new Date(note.mtime * 1000)

  return (
    <article className="note">
      <div className="note-head">
        <h1>{note.title}</h1>
        <div className="note-meta">
          {note.path} · {(note.size / 1024).toFixed(1)} КБ · seq {note.seq} ·{' '}
          {date.toLocaleString('ru-RU', { dateStyle: 'medium', timeStyle: 'short' })}
        </div>
      </div>

      {mode === 'read' ? (
        <div className="md" ref={ref} dangerouslySetInnerHTML={{ __html: html }} />
      ) : (
        <pre className="source">{note.content}</pre>
      )}
    </article>
  )
}

function Welcome({ error, count }: { error: string | null; count: number }) {
  return (
    <div className="empty-state">
      <h2>{error ? 'Сервер не отвечает' : 'Свод пуст'}</h2>
      {error ? (
        <p>{error}</p>
      ) : count > 0 ? (
        <p>Выбери заметку в дереве слева или нажми ⌘K.</p>
      ) : (
        <p>
          Запусти демона, он зальёт файлы:
          <br />
          <code>svod -vault ~/obsidian/Vk -server http://localhost:8080</code>
        </p>
      )}
    </div>
  )
}

// ───────────────────────── правая панель ─────────────────────────

function SidePanel({ note, onOpen }: { note: Note | null; onOpen: (p: string) => void }) {
  if (!note) {
    return (
      <>
        <p className="label">Структура</p>
        <div className="outline">
          <span className="empty">заметка не выбрана</span>
        </div>
      </>
    )
  }

  const scrollTo = (id: string) => {
    document.getElementById(id)?.scrollIntoView({ behavior: 'smooth', block: 'start' })
  }

  return (
    <>
      <div className="panel-section">
        <p className="label">Структура</p>
        <div className="outline">
          {note.headings.length === 0 && <span className="empty">заголовков нет</span>}
          {note.headings.map((h, i) => (
            <button key={`${h.id}-${i}`} className={`h${h.level}`} onClick={() => scrollTo(h.id)}>
              {h.text}
            </button>
          ))}
        </div>
      </div>

      <div className="panel-section">
        <p className="label">Ссылки сюда · {note.backlinks.length}</p>
        <div className="links">
          {note.backlinks.length === 0 && <span className="empty">никто не ссылается</span>}
          {note.backlinks.map((b) => (
            <button key={b} onClick={() => onOpen(b)} title={b}>
              {displayName(b)}
            </button>
          ))}
        </div>
      </div>

      <div className="panel-section">
        <p className="label">Теги · {note.tags.length}</p>
        <div className="tags">
          {note.tags.length === 0 && <span className="empty">нет</span>}
          {note.tags.map((t) => (
            <span key={t}>#{t}</span>
          ))}
        </div>
      </div>
    </>
  )
}

// ───────────────────────── статус ─────────────────────────

function StatusBar({
  online,
  seq,
  count,
  syncedAt,
  error,
}: {
  online: boolean
  seq: number
  count: number
  syncedAt: number
  error: string | null
}) {
  const [, tick] = useState(0)
  useEffect(() => {
    const id = window.setInterval(() => tick((n) => n + 1), 5000)
    return () => window.clearInterval(id)
  }, [])

  const dot = error ? 'danger' : online ? 'ok' : ''
  const text = error
    ? 'ошибка связи'
    : online
      ? `синхронизировано · ${ago(syncedAt)}`
      : 'офлайн · переподключаюсь'

  return (
    <footer className="status">
      <span className={`dot ${dot}`} />
      <span>{text}</span>
      <span className="sep">·</span>
      <span>
        {count} файлов · seq {seq}
      </span>
      <span className="push">
        <span>⌘K переход</span>
        <span>⌘⇧F поиск</span>
      </span>
    </footer>
  )
}

function ago(ts: number): string {
  const s = Math.max(0, Math.round((Date.now() - ts) / 1000))
  if (s < 5) return 'только что'
  if (s < 60) return `${s} сек назад`
  const m = Math.round(s / 60)
  if (m < 60) return `${m} мин назад`
  return `${Math.round(m / 60)} ч назад`
}

// ───────────────────────── ⌘K и ⌘⇧F ─────────────────────────

function Palette({
  kind,
  files,
  onClose,
  onOpen,
}: {
  kind: 'files' | 'search'
  files: FileMeta[]
  onClose: () => void
  onOpen: (p: string) => void
}) {
  const [q, setQ] = useState('')
  const [hits, setHits] = useState<SearchHit[]>([])
  const [active, setActive] = useState(0)
  const [busy, setBusy] = useState(false)

  // Поиск по содержимому ходит на сервер, переход по именам — локальный.
  useEffect(() => {
    if (kind !== 'search') return
    const query = q.trim()
    if (query === '') {
      setHits([])
      return
    }
    setBusy(true)
    const id = window.setTimeout(async () => {
      try {
        const res = await api.search(query)
        setHits(res.hits)
      } catch {
        setHits([])
      } finally {
        setBusy(false)
      }
    }, 180)
    return () => window.clearTimeout(id)
  }, [q, kind])

  const local = useMemo(() => {
    if (kind !== 'files') return []
    const needle = q.trim().toLowerCase()
    const list = needle
      ? files.filter((f) => f.path.toLowerCase().includes(needle))
      : files.slice()
    return list.slice(0, 50)
  }, [files, q, kind])

  const rows: { path: string; title: string; snippet?: string }[] =
    kind === 'files'
      ? local.map((f) => ({ path: f.path, title: f.title || displayName(f.path) }))
      : hits.map((h) => ({ path: h.path, title: h.title, snippet: h.snippet }))

  useEffect(() => setActive(0), [q])

  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setActive((i) => Math.min(i + 1, rows.length - 1))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setActive((i) => Math.max(i - 1, 0))
    } else if (e.key === 'Enter' && rows[active]) {
      e.preventDefault()
      onOpen(rows[active].path)
    }
  }

  return (
    <div className="overlay" onMouseDown={onClose}>
      <div className="palette" onMouseDown={(e) => e.stopPropagation()}>
        <input
          autoFocus
          value={q}
          onChange={(e) => setQ(e.target.value)}
          onKeyDown={onKey}
          placeholder={kind === 'files' ? 'Перейти к заметке…' : 'Найти в тексте свода…'}
        />
        <div className="palette-list">
          {rows.map((r, i) => (
            <button
              key={r.path}
              className={`hit ${i === active ? 'is-active' : ''}`}
              onClick={() => onOpen(r.path)}
              onMouseEnter={() => setActive(i)}
            >
              <div className="t">{r.title}</div>
              <div className="p">{r.path}</div>
              {r.snippet && <div className="s">{highlight(r.snippet)}</div>}
            </button>
          ))}
        </div>
        <div className="palette-foot">
          {busy
            ? 'ищу…'
            : rows.length > 0
              ? `${rows.length} · ↑↓ выбрать · ⏎ открыть · Esc закрыть`
              : q.trim()
                ? 'ничего не нашлось'
                : 'начни печатать'}
        </div>
      </div>
    </div>
  )
}

/** Сервер размечает совпадения квадратными скобками — превращаем их в <mark>. */
function highlight(snippet: string) {
  const parts = snippet.split(/(\[[^\]]*\])/g)
  return parts.map((p, i) =>
    p.startsWith('[') && p.endsWith(']') ? <mark key={i}>{p.slice(1, -1)}</mark> : <span key={i}>{p}</span>,
  )
}
