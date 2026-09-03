import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  api,
  ConflictError,
  UnauthorizedError,
  openStream,
  rawURL,
  type FileMeta,
  type Note,
  type SearchHit,
  type Version,
} from './api'
import type { EditorHandle } from './editor'
import { ensureMath, hasMath, headingId, renderNote } from './md'

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

/**
 * Ищем цель ссылки или вложения: сначала по полному пути,
 * потом по имени файла — так пишут в Obsidian.
 */
function resolveTarget(files: FileMeta[], target: string): string | null {
  const clean = target.replace(/^\.?\//, '')
  const exact = files.find((f) => f.path === clean || f.path.replace(/\.md$/i, '') === clean.replace(/\.md$/i, ''))
  if (exact) return exact.path

  const base = (clean.split('/').pop() ?? '').toLowerCase()
  const byName = files.find((f) => {
    const n = (f.path.split('/').pop() ?? '').toLowerCase()
    return n === base || n.replace(/\.md$/i, '') === base.replace(/\.md$/i, '')
  })
  return byName ? byName.path : null
}

function displayName(path: string): string {
  return (path.split('/').pop() ?? path).replace(/\.md$/i, '')
}

function isImage(path: string): boolean {
  return /\.(png|jpe?g|gif|webp|svg)$/i.test(path)
}

// ───────────────────────── приложение ─────────────────────────

type Overlay = null | 'files' | 'search'
type Mode = 'read' | 'edit'
type Theme = 'dark' | 'light'

const THEME_KEY = 'svod.theme'

export default function App() {
  const [files, setFiles] = useState<FileMeta[]>([])
  const [seq, setSeq] = useState(0)
  const [online, setOnline] = useState(false)
  const [syncedAt, setSyncedAt] = useState<number>(Date.now())
  const [error, setError] = useState<string | null>(null)

  const [path, setPath] = useState<string | null>(null)
  const [note, setNote] = useState<Note | null>(null)
  const [mode, setMode] = useState<Mode>('read')
  // Черновик живёт здесь, а не внутри редактора: при выходе из режима
  // правки React уничтожает редактор раньше, чем успевает сработать
  // сохранение, и текст было бы некому спасти.
  const [draft, setDraft] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [conflict, setConflict] = useState<{ mine: string; serverHash: string } | null>(null)

  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [showLeft, setShowLeft] = useState(true)
  const [showRight, setShowRight] = useState(true)
  const [overlay, setOverlay] = useState<Overlay>(null)
  const [theme, setTheme] = useState<Theme>(readTheme)
  // null — ещё не спросили у сервера; false — вход нужен.
  const [authed, setAuthed] = useState<boolean | null>(null)

  const editorRef = useRef<EditorHandle | null>(null)
  const tree = useMemo(() => buildTree(files), [files])

  const dirty = note !== null && draft !== null && draft !== note.content

  // Актуальное значение в ссылке: иначе open() замыкает состояние того
  // рендера, в котором был создан, и спрашивает про правки, которых уже нет.
  const dirtyRef = useRef(dirty)
  useEffect(() => {
    dirtyRef.current = dirty
  })

  // Тему держим на корне документа — токены переключаются сами.
  useEffect(() => {
    document.documentElement.dataset.theme = theme
    try {
      localStorage.setItem(THEME_KEY, theme)
    } catch {
      // приватное окно — просто не запомним
    }
  }, [theme])

  const loadTree = useCallback(async () => {
    try {
      const t = await api.tree()
      setFiles(t.files)
      setSeq(t.seq)
      setAuthed(true)
      setError(null)
    } catch (e) {
      if (e instanceof UnauthorizedError) {
        setAuthed(false)
        return
      }
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [])

  const open = useCallback(
    async (p: string, force = false) => {
      if (!force && dirtyRef.current && !confirm('Есть несохранённые правки. Уйти и потерять их?')) {
        return
      }
      setPath(p)
      setOverlay(null)
      setMode('read')
      setDraft(null)
      setConflict(null)
      try {
        setNote(await api.note(p))
        setError(null)
      } catch (e) {
        setNote(null)
        setError(e instanceof Error ? e.message : String(e))
      }
    },
    [],
  )

  // Сохранение. Конфликт не молчаливый: сервер ничего не перезаписал,
  // и пользователь решает, что делать со своей версией.
  const save = useCallback(async () => {
    if (!note || saving) return
    const text = draft
    if (text === null || text === note.content) return

    setSaving(true)
    try {
      const res = await api.save(note.path, text, note.hash)
      setNote((n) => (n ? { ...n, content: text, hash: res.hash, seq: res.seq } : n))
      setDraft(null)
      setConflict(null)
      setSyncedAt(Date.now())
      void loadTree()
    } catch (e) {
      if (e instanceof ConflictError) {
        setConflict({ mine: text, serverHash: e.serverHash })
      } else {
        setError(e instanceof Error ? e.message : String(e))
      }
    } finally {
      setSaving(false)
    }
  }, [note, draft, saving, loadTree])

  // Разрешение конфликта: своя версия уходит отдельным файлом,
  // ровно как это делает демон на диске.
  const saveAsCopy = useCallback(async () => {
    if (!note || !conflict) return
    const stamp = new Date()
      .toISOString()
      .slice(0, 16)
      .replace('T', ' ')
    const copyPath = note.path.replace(/(\.md)?$/i, ` (конфликт, браузер, ${stamp})$1`)
    try {
      await api.save(copyPath, conflict.mine, '')
      setConflict(null)
      setDraft(null)
      await loadTree()
      await open(copyPath, true)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [note, conflict, loadTree, open])

  const reloadFromServer = useCallback(async () => {
    if (!path) return
    setConflict(null)
    setDraft(null)
    try {
      const fresh = await api.note(path)
      setNote(fresh)
      editorRef.current?.setValue(fresh.content)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [path])

  // Сначала выясняем, нужен ли вход: без него всё остальное вернёт 401.
  useEffect(() => {
    void api
      .authState()
      .then((st) => setAuthed(st.authorized))
      .catch(() => setAuthed(false))
  }, [])

  // Данные и живой поток — только после входа.
  useEffect(() => {
    if (authed !== true) return
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
  }, [loadTree, authed])

  // Заметка обновляется, когда её версия уехала вперёд — но не поверх
  // несохранённых правок.
  useEffect(() => {
    if (!path || dirty || mode === 'edit') return
    const meta = files.find((f) => f.path === path)
    if (meta && note && meta.hash !== note.hash) {
      void api.note(path).then(setNote).catch(() => undefined)
    }
  }, [files, path, note, dirty, mode])

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
        setMode((m) => (m === 'read' ? 'edit' : 'read'))
      } else if (k === 's') {
        e.preventDefault()
        void save()
      } else if (k === '\\') {
        e.preventDefault()
        setShowLeft((v) => !v)
      } else if (k === '.') {
        e.preventDefault()
        setShowRight((v) => !v)
      } else if (k === 'l' && e.shiftKey) {
        e.preventDefault()
        setTheme((t) => (t === 'dark' ? 'light' : 'dark'))
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [save])

  // Уход со страницы с несохранённым текстом.
  useEffect(() => {
    if (!dirty) return
    const warn = (e: BeforeUnloadEvent) => e.preventDefault()
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [dirty])

  // Выход из режима правки сохраняет молча — терять текст нельзя.
  useEffect(() => {
    if (mode === 'read' && dirty) void save()
  }, [mode, dirty, save])

  const bodyClass = ['body', showLeft ? '' : 'no-left', showRight ? '' : 'no-right']
    .filter(Boolean)
    .join(' ')

  const editable = note !== null && !note.binary

  if (authed === null) {
    return <div className="boot">Свод открывается…</div>
  }
  if (authed === false) {
    return <Login onDone={() => setAuthed(true)} theme={theme} />
  }

  return (
    <div className="app">
      <TopBar
        path={path}
        mode={mode}
        editable={editable}
        dirty={dirty}
        saving={saving}
        theme={theme}
        onMode={setMode}
        onTheme={() => setTheme((t) => (t === 'dark' ? 'light' : 'dark'))}
      />

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
          {conflict && (
            <ConflictBar onReload={reloadFromServer} onCopy={saveAsCopy} />
          )}
          {note ? (
            <NoteView
              note={note}
              mode={mode}
              files={files}
              initial={draft ?? note.content}
              onOpen={open}
              onChange={setDraft}
              onSave={save}
              editorRef={editorRef}
            />
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
        dirty={dirty}
        conflict={conflict !== null}
      />

      {overlay && (
        <Palette kind={overlay} files={files} onClose={() => setOverlay(null)} onOpen={open} />
      )}
    </div>
  )
}

function Login({ onDone, theme }: { onDone: () => void; theme: Theme }) {
  const [token, setToken] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  useEffect(() => {
    document.documentElement.dataset.theme = theme
  }, [theme])

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (busy || token.trim() === '') return
    setBusy(true)
    setErr(null)
    try {
      await api.login(token)
      onDone()
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="login">
      <form className="login-card" onSubmit={submit}>
        <h1>Свод</h1>
        <p className="login-hint">
          Введи токен доступа. Он лежит в <code>deploy/.svod-token</code> —
          тот же, которым ходит демон.
        </p>
        <input
          autoFocus
          type="password"
          value={token}
          onChange={(e) => setToken(e.target.value)}
          placeholder="Токен"
          spellCheck={false}
          autoComplete="current-password"
        />
        {err && <p className="login-err">{err}</p>}
        <button type="submit" disabled={busy || token.trim() === ''}>
          {busy ? 'Проверяю…' : 'Войти'}
        </button>
      </form>
    </div>
  )
}

function readTheme(): Theme {
  try {
    const v = localStorage.getItem(THEME_KEY)
    if (v === 'light' || v === 'dark') return v
  } catch {
    // приватное окно
  }
  return 'dark'
}

// ───────────────────────── шапка ─────────────────────────

function TopBar({
  path,
  mode,
  editable,
  dirty,
  saving,
  theme,
  onMode,
  onTheme,
}: {
  path: string | null
  mode: Mode
  editable: boolean
  dirty: boolean
  saving: boolean
  theme: Theme
  onMode: (m: Mode) => void
  onTheme: () => void
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
        {dirty && <span className="kbd unsaved">{saving ? 'сохраняю…' : 'не сохранено'}</span>}
        {editable && (
          <>
            <button
              className={`pill ${mode === 'edit' ? 'is-on' : ''}`}
              onClick={() => onMode(mode === 'read' ? 'edit' : 'read')}
            >
              {mode === 'read' ? 'ЧТЕНИЕ' : 'ПРАВКА'}
            </button>
            <span className="kbd">⌘E</span>
          </>
        )}
        <button className="pill" onClick={onTheme} title="Тема · ⌘⇧L">
          {theme === 'dark' ? '墨' : '生'}
        </button>
      </span>
    </header>
  )
}

function ConflictBar({ onReload, onCopy }: { onReload: () => void; onCopy: () => void }) {
  return (
    <div className="conflict-bar">
      <b>Не сохранено: на сервере другая версия.</b> Твой текст цел и никуда
      не делся — выбери, что с ним сделать.
      <span className="conflict-actions">
        <button onClick={onCopy}>Сохранить своей копией</button>
        <button onClick={onReload}>Взять версию сервера</button>
      </span>
    </div>
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
  initial,
  onOpen,
  onChange,
  onSave,
  editorRef,
}: {
  note: Note
  mode: Mode
  files: FileMeta[]
  initial: string
  onOpen: (p: string) => void
  onChange: (text: string) => void
  onSave: () => void
  editorRef: React.MutableRefObject<EditorHandle | null>
}) {
  const readRef = useRef<HTMLDivElement>(null)
  const editRef = useRef<HTMLDivElement>(null)

  // KaTeX подтягивается только для заметок с формулами: со шрифтами он
  // весит четыре мегабайта, и ради обычного текста это перебор.
  const [mathReady, setMathReady] = useState(false)
  const needsMath = useMemo(() => hasMath(note.content), [note.content])

  useEffect(() => {
    if (!needsMath || mathReady) return
    let alive = true
    void ensureMath().then(() => alive && setMathReady(true))
    return () => {
      alive = false
    }
  }, [needsMath, mathReady])

  // mathReady в зависимостях намеренно: после загрузки KaTeX заметку
  // нужно перерисовать, иначе формулы останутся исходниками.
  const html = useMemo(() => renderNote(note.content), [note.content, mathReady])

  // Свежие обработчики держим в ссылках, чтобы редактор не пересоздавался
  // на каждое сохранение: пересоздание сбрасывает курсор и историю отмен.
  const latest = useRef({ initial, onChange, onSave })
  useEffect(() => {
    latest.current = { initial, onChange, onSave }
  })

  // Якоря заголовкам, адреса вложениям, переходы по wiki-ссылкам.
  useEffect(() => {
    const el = readRef.current
    if (!el || mode !== 'read') return

    const seen = new Map<string, number>()
    el.querySelectorAll('h1, h2, h3, h4, h5, h6').forEach((h) => {
      const base = headingId(h.textContent ?? '')
      const n = seen.get(base) ?? 0
      seen.set(base, n + 1)
      h.id = n === 0 ? base : `${base}-${n}`
    })

    // Картинки приходят с data-target вместо src: путь надо разрешить
    // по дереву свода, как это делает Obsidian.
    el.querySelectorAll('img.embed').forEach((img) => {
      const target = img.getAttribute('data-target')
      if (!target) return
      const resolved = resolveTarget(files, target)
      if (resolved) {
        img.setAttribute('src', rawURL(resolved))
      } else {
        img.replaceWith(
          Object.assign(document.createElement('span'), {
            className: 'missing',
            textContent: `нет вложения: ${target}`,
          }),
        )
      }
    })

    const onClick = (e: MouseEvent) => {
      const link = (e.target as HTMLElement).closest('a.wikilink')
      if (!link) return
      e.preventDefault()
      const target = link.getAttribute('data-target') ?? ''
      const resolved = resolveTarget(files, target)
      if (resolved) onOpen(resolved)
    }
    el.addEventListener('click', onClick)
    return () => el.removeEventListener('click', onClick)
  }, [html, mode, files, onOpen])

  // Редактор поднимаем лениво: в режиме чтения он не нужен.
  // Пересоздаём только при смене режима или заметки.
  useEffect(() => {
    if (mode !== 'edit' || !editRef.current) return
    let handle: EditorHandle | null = null
    let cancelled = false

    void import('./editor').then(({ createEditor }) => {
      if (cancelled || !editRef.current) return
      handle = createEditor(
        editRef.current,
        latest.current.initial,
        (text) => latest.current.onChange(text),
        () => latest.current.onSave(),
      )
      editorRef.current = handle
      handle.view.focus()
    })

    return () => {
      cancelled = true
      handle?.destroy()
      editorRef.current = null
    }
  }, [mode, note.path, editorRef])

  // Вставка картинки из буфера: файл уезжает в папку вложений,
  // а в текст встаёт ссылка на него.
  useEffect(() => {
    if (mode !== 'edit') return
    const onPaste = async (e: ClipboardEvent) => {
      const item = Array.from(e.clipboardData?.items ?? []).find((i) =>
        i.type.startsWith('image/'),
      )
      if (!item) return
      const file = item.getAsFile()
      if (!file) return
      e.preventDefault()

      const ext = (file.type.split('/')[1] ?? 'png').replace('jpeg', 'jpg')
      const stamp = new Date().toISOString().replace(/[-:T]/g, '').slice(0, 14)
      const name = `Вложения/Вставка ${stamp}.${ext}`
      try {
        await api.save(name, file, '')
        editorRef.current?.insert(`![[${name}]]`)
      } catch (err) {
        alert(`Не смог сохранить картинку: ${err instanceof Error ? err.message : err}`)
      }
    }
    window.addEventListener('paste', onPaste)
    return () => window.removeEventListener('paste', onPaste)
  }, [mode, editorRef])

  const date = new Date(note.mtime * 1000)

  if (note.binary) {
    return (
      <article className="note">
        <div className="note-head">
          <h1>{displayName(note.path)}</h1>
          <div className="note-meta">
            {note.path} · {(note.size / 1024).toFixed(1)} КБ · seq {note.seq}
          </div>
        </div>
        {isImage(note.path) ? (
          <img className="attachment" src={rawURL(note.path)} alt={note.path} />
        ) : (
          <p className="empty-note">
            Вложение. <a href={rawURL(note.path)}>Открыть в новой вкладке</a>
          </p>
        )}
      </article>
    )
  }

  return (
    <article className="note">
      <div className="note-head">
        <h1>{note.title}</h1>
        <div className="note-meta">
          {note.path} · {(note.size / 1024).toFixed(1)} КБ · seq {note.seq} ·{' '}
          {date.toLocaleString('ru-RU', { dateStyle: 'medium', timeStyle: 'short' })}
        </div>
        {note.meta && Object.keys(note.meta).length > 0 && (
          <div className="frontmatter">
            {Object.entries(note.meta).map(([k, v]) => (
              <span key={k}>
                <i>{k}</i> {v}
              </span>
            ))}
          </div>
        )}
      </div>

      {mode === 'read' ? (
        <div className="md" ref={readRef} dangerouslySetInnerHTML={{ __html: html }} />
      ) : (
        <div className="editor" ref={editRef} />
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
          <code>make daemon VAULT=~/obsidian/Vk</code>
        </p>
      )}
    </div>
  )
}

// ───────────────────────── правая панель ─────────────────────────

function SidePanel({ note, onOpen }: { note: Note | null; onOpen: (p: string) => void }) {
  const [versions, setVersions] = useState<Version[]>([])

  useEffect(() => {
    if (!note) {
      setVersions([])
      return
    }
    let alive = true
    void api
      .history(note.path)
      .then((r) => alive && setVersions(r.versions))
      .catch(() => alive && setVersions([]))
    return () => {
      alive = false
    }
  }, [note?.path, note?.hash])

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
      {!note.binary && (
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
      )}

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

      {!note.binary && (
        <div className="panel-section">
          <p className="label">Теги · {note.tags.length}</p>
          <div className="tags">
            {note.tags.length === 0 && <span className="empty">нет</span>}
            {note.tags.map((t) => (
              <span key={t}>#{t}</span>
            ))}
          </div>
        </div>
      )}

      <div className="panel-section">
        <p className="label">История · {versions.length}</p>
        <div className="history">
          {versions.length === 0 && <span className="empty">одна версия</span>}
          {versions.map((v) => (
            <div key={v.seq} className={v.hash === note.hash ? 'is-current' : ''}>
              <span className="seq">seq {v.seq}</span>
              <span className="when">
                {new Date(v.at * 1000).toLocaleString('ru-RU', {
                  dateStyle: 'short',
                  timeStyle: 'short',
                })}
              </span>
              <span className="dev">{v.device}</span>
            </div>
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
  dirty,
  conflict,
}: {
  online: boolean
  seq: number
  count: number
  syncedAt: number
  error: string | null
  dirty: boolean
  conflict: boolean
}) {
  const [, tick] = useState(0)
  useEffect(() => {
    const id = window.setInterval(() => tick((n) => n + 1), 5000)
    return () => window.clearInterval(id)
  }, [])

  let dot = ''
  let text = 'офлайн · переподключаюсь'
  if (conflict) {
    dot = 'danger'
    text = 'конфликт — версия не сохранена'
  } else if (error) {
    dot = 'danger'
    text = 'ошибка связи'
  } else if (dirty) {
    dot = 'warn'
    text = 'есть несохранённые правки'
  } else if (online) {
    dot = 'ok'
    text = `синхронизировано · ${ago(syncedAt)}`
  }

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
        <span>⌘S сохранить</span>
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
    const list = needle ? files.filter((f) => f.path.toLowerCase().includes(needle)) : files.slice()
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
    p.startsWith('[') && p.endsWith(']') ? (
      <mark key={i}>{p.slice(1, -1)}</mark>
    ) : (
      <span key={i}>{p}</span>
    ),
  )
}
