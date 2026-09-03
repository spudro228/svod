// Адреса страниц.
//
// Роутера нет намеренно: у приложения два состояния — список и заметка.
// history.pushState со слушателем popstate занимает полсотни строк,
// а библиотека принесла бы полтора десятка килобайт и свою модель мышления.

const PREFIX = '/n/'
const SCROLL_KEY = 'svod.scroll:'

/** Адрес заметки. Каждый сегмент кодируется отдельно: разделители остаются. */
export function noteURL(path: string, anchor?: string): string {
  const url = PREFIX + path.split('/').map(encodeURIComponent).join('/')
  return anchor ? `${url}#${encodeURIComponent(anchor)}` : url
}

/** Какая заметка открыта по текущему адресу. */
export function pathFromLocation(): string | null {
  if (!location.pathname.startsWith(PREFIX)) return null
  const raw = location.pathname.slice(PREFIX.length)
  if (raw === '') return null
  try {
    return raw.split('/').map(decodeURIComponent).join('/')
  } catch {
    return null // битая процентная кодировка — считаем, что заметки нет
  }
}

/** Якорь на заголовок внутри заметки. */
export function anchorFromLocation(): string {
  if (!location.hash) return ''
  try {
    return decodeURIComponent(location.hash.slice(1))
  } catch {
    return ''
  }
}

export function pushNote(path: string, anchor?: string): void {
  const url = noteURL(path, anchor)
  if (url !== location.pathname + location.hash) history.pushState(null, '', url)
}

export function replaceAnchor(path: string, anchor: string): void {
  history.replaceState(null, '', noteURL(path, anchor))
}

/**
 * Точное смещение внутри заметки. Живёт в sessionStorage, а не в адресе:
 * это удобство одного зрителя в одной вкладке, а не часть ссылки.
 * Заметку могли поправить с другой машины, поэтому смещение — подсказка,
 * а якорь на заголовок надёжнее.
 */
export function saveScroll(path: string, top: number): void {
  try {
    sessionStorage.setItem(SCROLL_KEY + path, String(Math.round(top)))
  } catch {
    // приватное окно — просто не запомним
  }
}

export function loadScroll(path: string): number {
  try {
    const v = sessionStorage.getItem(SCROLL_KEY + path)
    return v ? Number(v) : 0
  } catch {
    return 0
  }
}
