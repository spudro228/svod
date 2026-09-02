// Рендер markdown: markdown-it плюс три правила под наш синтаксис.
//
// Плагины работают на уровне токенов, поэтому содержимое блоков кода
// не трогается: [[ссылка]] внутри ``` остаётся текстом.

import MarkdownIt from 'markdown-it'
import type { PluginSimple } from 'markdown-it'

/** [[путь/файл]] и [[путь/файл|подпись]] */
const wikilinks: PluginSimple = (md) => {
  md.inline.ruler.before('link', 'wikilink', (state, silent) => {
    const start = state.pos
    if (state.src.charCodeAt(start) !== 0x5b) return false // [
    if (state.src.charCodeAt(start + 1) !== 0x5b) return false

    const end = state.src.indexOf(']]', start + 2)
    if (end < 0) return false

    const inner = state.src.slice(start + 2, end)
    if (inner.includes('[') || inner.includes('\n') || inner.trim() === '') return false

    if (!silent) {
      const bar = inner.indexOf('|')
      const target = (bar < 0 ? inner : inner.slice(0, bar)).trim()
      const label = (bar < 0 ? inner : inner.slice(bar + 1)).trim() || target
      const token = state.push('wikilink', '', 0)
      token.content = label
      token.meta = { target }
    }
    state.pos = end + 2
    return true
  })

  md.renderer.rules.wikilink = (tokens, idx) => {
    const t = tokens[idx]
    const target = md.utils.escapeHtml((t.meta as { target: string }).target)
    return `<a class="wikilink" data-target="${target}">${md.utils.escapeHtml(t.content)}</a>`
  }
}

/** #тег — только в начале строки или после пробела */
const tags: PluginSimple = (md) => {
  const re = /^#([\p{L}\d][\p{L}\d_/-]*)/u

  md.inline.ruler.before('emphasis', 'svodtag', (state, silent) => {
    const pos = state.pos
    if (state.src.charCodeAt(pos) !== 0x23) return false // #
    if (pos > 0 && !/[\s(]/.test(state.src[pos - 1])) return false

    const m = re.exec(state.src.slice(pos))
    if (!m) return false

    if (!silent) {
      const token = state.push('svodtag', '', 0)
      token.content = m[1]
    }
    state.pos += m[0].length
    return true
  })

  md.renderer.rules.svodtag = (tokens, idx) =>
    `<span class="tag">#${md.utils.escapeHtml(tokens[idx].content)}</span>`
}

/**
 * Списки задач. Отметку не вставляем в разметку — вешаем класс на <li>,
 * а квадрат рисует CSS через ::before. Так работает и в плотных списках,
 * где markdown-it прячет параграф.
 */
const tasklists: PluginSimple = (md) => {
  md.core.ruler.after('inline', 'svodtasks', (state) => {
    const tokens = state.tokens
    for (let i = 2; i < tokens.length; i++) {
      if (tokens[i].type !== 'inline') continue
      if (tokens[i - 1].type !== 'paragraph_open') continue
      const li = tokens[i - 2]
      if (li.type !== 'list_item_open') continue

      const m = /^\[([ xX])\]\s+/.exec(tokens[i].content)
      if (!m) continue

      tokens[i].content = tokens[i].content.slice(m[0].length)
      const first = tokens[i].children?.[0]
      if (first && first.type === 'text') {
        first.content = first.content.replace(/^\[([ xX])\]\s+/, '')
      }
      li.attrJoin('class', m[1] === ' ' ? 'task' : 'task done')
    }
    return true
  })
}

const md = new MarkdownIt({
  html: false, // содержимое свода не доверяем как HTML
  linkify: true,
  typographer: false,
  breaks: false,
})
  .use(wikilinks)
  .use(tags)
  .use(tasklists)

/**
 * Рендер заметки. Первый H1 выбрасываем: он уже показан в шапке,
 * иначе заголовок дублируется.
 */
export function renderNote(source: string): string {
  const withoutTitle = source.replace(/^﻿?\s*#\s+[^\n]*\n?/, '')
  return md.render(withoutTitle)
}

/** Заголовки для якорей в оглавлении. */
export function headingId(text: string): string {
  return text
    .toLowerCase()
    .trim()
    .replace(/[^\p{L}\p{N}\s-]/gu, '')
    .replace(/[\s-]+/g, '-')
    .replace(/^-|-$/g, '')
}
