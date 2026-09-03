// Рендер markdown: markdown-it плюс три правила под наш синтаксис.
//
// Плагины работают на уровне токенов, поэтому содержимое блоков кода
// не трогается: [[ссылка]] внутри ``` остаётся текстом.

import MarkdownIt from 'markdown-it'
import type { PluginSimple } from 'markdown-it'
import type KatexNamespace from 'katex'

// ───────────────────────── формулы ─────────────────────────
//
// KaTeX со шрифтами весит четыре мегабайта, а формулы есть в считанных
// заметках. Поэтому он подгружается лениво: до загрузки формула
// показывается исходником, после — набирается по-настоящему.

let katex: typeof KatexNamespace | null = null

/** Есть ли в тексте формулы — чтобы не тащить KaTeX ради обычной заметки. */
export function hasMath(src: string): boolean {
  return /\$\$[\s\S]+?\$\$/.test(src) || /(?<!\$)\$(?!\$)[^\n$]+\$(?!\$)/.test(src)
}

/** Подгружает KaTeX вместе с его стилями. Повторные вызовы бесплатны. */
export async function ensureMath(): Promise<void> {
  if (katex) return
  const [mod] = await Promise.all([import('katex'), import('katex/dist/katex.min.css')])
  katex = mod.default
}

function renderTex(md: MarkdownIt, tex: string, display: boolean): string {
  if (!katex) {
    return `<code class="math-raw">${md.utils.escapeHtml(tex)}</code>`
  }
  try {
    return katex.renderToString(tex, {
      displayMode: display,
      throwOnError: false,
      strict: false,
      output: 'html',
    })
  } catch {
    // Битую формулу показываем как есть: потерять её хуже, чем показать сырой.
    return `<code class="math-err" title="не разобрал формулу">${md.utils.escapeHtml(tex)}</code>`
  }
}

/**
 * $…$ и $$…$$.
 *
 * Правила заимствованы у общепринятых реализаций и нужны, чтобы не портить
 * обычный текст: после открывающего доллара не должно быть пробела, перед
 * закрывающим тоже, а за закрывающим не должно идти цифры — иначе
 * «заплатил $5 и $10» превратилось бы в формулу.
 */
const math: PluginSimple = (md) => {
  // Блочные формулы: $$ в начале строки, возможно на несколько строк.
  md.block.ruler.before('fence', 'math_block', (state, startLine, endLine, silent) => {
    const start = state.bMarks[startLine] + state.tShift[startLine]
    const max = state.eMarks[startLine]
    if (start + 2 > max) return false
    if (state.src.slice(start, start + 2) !== '$$') return false

    let line = startLine
    let found = false
    let content = ''

    const firstLine = state.src.slice(start + 2, max)
    if (firstLine.trimEnd().endsWith('$$')) {
      content = firstLine.trimEnd().slice(0, -2)
      found = true
    } else {
      content = firstLine
      while (!found && ++line < endLine) {
        const from = state.bMarks[line] + state.tShift[line]
        const to = state.eMarks[line]
        const text = state.src.slice(from, to)
        if (text.trimEnd().endsWith('$$')) {
          content += '\n' + text.trimEnd().slice(0, -2)
          found = true
        } else {
          content += '\n' + text
        }
      }
    }
    if (!found) return false
    if (silent) return true

    const token = state.push('math_block', '', 0)
    token.content = content.trim()
    token.map = [startLine, line + 1]
    state.line = line + 1
    return true
  })

  md.inline.ruler.after('escape', 'math_inline', (state, silent) => {
    const src = state.src
    const pos = state.pos
    if (src.charCodeAt(pos) !== 0x24) return false // $

    const display = src.charCodeAt(pos + 1) === 0x24
    const marker = display ? '$$' : '$'
    const from = pos + marker.length

    // Формула не переносится через строку — иначе доллары из соседних
    // абзацев склеились бы в одну «формулу».
    const nl = src.indexOf('\n', from)
    const limit = nl < 0 ? src.length : nl
    const end = src.indexOf(marker, from)
    if (end < 0 || end >= limit) return false

    const tex = src.slice(from, end)
    if (tex.trim() === '') return false
    if (!display) {
      if (/\s/.test(tex[0]) || /\s/.test(tex[tex.length - 1])) return false
      if (/\d/.test(src[end + 1] ?? '')) return false
    }

    if (!silent) {
      const token = state.push('math_inline', '', 0)
      token.content = tex
      token.markup = marker
    }
    state.pos = end + marker.length
    return true
  })

  md.renderer.rules.math_block = (tokens, idx) =>
    `<div class="math-block">${renderTex(md, tokens[idx].content, true)}</div>`

  md.renderer.rules.math_inline = (tokens, idx) =>
    renderTex(md, tokens[idx].content, tokens[idx].markup === '$$')
}

/**
 * [[путь/файл]], [[путь/файл|подпись]] и ![[вложение.png]].
 * Восклицательный знак впереди означает встраивание, а не ссылку.
 */
const wikilinks: PluginSimple = (md) => {
  md.inline.ruler.before('link', 'wikilink', (state, silent) => {
    let start = state.pos
    let embed = false
    if (state.src.charCodeAt(start) === 0x21) {
      // !
      embed = true
      start++
    }
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
      token.meta = { target, embed }
    }
    state.pos = end + 2
    return true
  })

  md.renderer.rules.wikilink = (tokens, idx) => {
    const tok = tokens[idx]
    const { target, embed } = tok.meta as { target: string; embed: boolean }
    const safe = md.utils.escapeHtml(target)
    if (embed) {
      // Настоящий адрес подставит страница: путь надо сперва разрешить
      // по дереву свода.
      return `<img class="embed" data-target="${safe}" alt="${md.utils.escapeHtml(tok.content)}">`
    }
    return `<a class="wikilink" data-target="${safe}">${md.utils.escapeHtml(tok.content)}</a>`
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

/** Обычные картинки markdown тоже указывают на файлы свода. */
const images: PluginSimple = (md) => {
  const base = md.renderer.rules.image
  md.renderer.rules.image = (tokens, idx, opts, env, self) => {
    const tok = tokens[idx]
    const src = tok.attrGet('src') ?? ''
    if (!/^(https?:|data:|\/)/.test(src)) {
      // Относительный путь — пусть страница разрешит его по дереву.
      tok.attrSet('data-target', decodeURIComponent(src))
      tok.attrSet('src', '')
      tok.attrJoin('class', 'embed')
    }
    return base ? base(tokens, idx, opts, env, self) : self.renderToken(tokens, idx, opts)
  }
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
  .use(images)
  .use(math)

/**
 * Рендер заметки.
 *
 * Frontmatter и первый H1 выбрасываем: и то и другое уже показано
 * в шапке заметки, иначе задваивается.
 */
export function renderNote(source: string): string {
  const withoutMeta = source.replace(/^﻿?---\r?\n[\s\S]*?\r?\n---[ \t]*\r?\n?/, '')
  const withoutTitle = withoutMeta.replace(/^﻿?\s*#\s+[^\n]*\n?/, '')
  return md.render(withoutTitle)
}

/** Есть ли у заметки frontmatter — чтобы не резать первую строку зря. */
export function stripFrontmatter(source: string): string {
  return source.replace(/^﻿?---\r?\n[\s\S]*?\r?\n---[ \t]*\r?\n?/, '')
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
