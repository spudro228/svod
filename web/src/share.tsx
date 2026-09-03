// Страница для гостя: одна заметка по временной ссылке.
//
// Отдельная сборка, а не приложение с флагом «только чтение». Так в неё
// физически не попадёт вызов, который спросит дерево свода или поиск:
// здесь просто нет такого кода.

import { useEffect, useState } from 'react'
import { createRoot } from 'react-dom/client'
import './tokens.css'
import './app.css'
import './share.css'
import { ensureMath, hasMath, renderNote } from './md'

type SharedNote = {
  path: string
  title: string
  content: string
  mtime: number
  size: number
}

function keyFromLocation(): string {
  const m = location.pathname.match(/^\/s\/([^/]+)/)
  return m ? m[1] : ''
}

function Shared() {
  const [note, setNote] = useState<SharedNote | null>(null)
  const [expires, setExpires] = useState(0)
  const [error, setError] = useState<string | null>(null)
  const [mathReady, setMathReady] = useState(false)

  const key = keyFromLocation()

  useEffect(() => {
    if (!key) {
      setError('Ссылка не распознана')
      return
    }
    void fetch(`/api/v1/shared/${encodeURIComponent(key)}`)
      .then(async (res) => {
        if (!res.ok) throw new Error('Ссылка недействительна или истекла')
        return (await res.json()) as { note: SharedNote; expires: number }
      })
      .then((data) => {
        setNote(data.note)
        setExpires(data.expires)
      })
      .catch((e) => setError(e instanceof Error ? e.message : String(e)))
  }, [key])

  useEffect(() => {
    if (!note || mathReady || !hasMath(note.content)) return
    void ensureMath().then(() => setMathReady(true))
  }, [note, mathReady])

  // Картинки идут через ту же ссылку: другие вложения по ней недоступны.
  useEffect(() => {
    if (!note) return
    document.querySelectorAll<HTMLImageElement>('.md img.embed').forEach((img) => {
      const target = img.getAttribute('data-target')
      if (!target) return
      const encoded = target.split('/').map(encodeURIComponent).join('/')
      img.src = `/api/v1/shared/${encodeURIComponent(key)}/asset/${encoded}`
    })
    // Переходы по wiki-ссылкам гостю недоступны: показываем их обычным текстом.
    document.querySelectorAll('.md a.wikilink').forEach((a) => {
      a.classList.add('is-dead')
      a.removeAttribute('href')
    })
  }, [note, mathReady, key])

  if (error) {
    return (
      <div className="guest">
        <div className="guest-card">
          <h1>Ссылка не работает</h1>
          <p>{error}</p>
        </div>
      </div>
    )
  }

  if (!note) {
    return <div className="boot">Открываю…</div>
  }

  const until = new Date(expires * 1000)

  return (
    <div className="guest">
      <article className="guest-note">
        <h1>{note.title}</h1>
        <div className="guest-meta">
          {new Date(note.mtime * 1000).toLocaleDateString('ru-RU', { dateStyle: 'long' })}
        </div>
        <div className="md" dangerouslySetInnerHTML={{ __html: renderNote(note.content) }} />
        <footer className="guest-foot">
          Временная ссылка из Свода. Действует до{' '}
          {until.toLocaleString('ru-RU', { dateStyle: 'medium', timeStyle: 'short' })}.
        </footer>
      </article>
    </div>
  )
}

const root = document.getElementById('root')
if (!root) throw new Error('нет элемента #root')
createRoot(root).render(<Shared />)
