// Редактор на CodeMirror 6.
//
// Грузится лениво: в режиме чтения он не нужен, а весит прилично.
// Тема собрана из тех же токенов, что и остальной интерфейс, — цветов
// в коде нет, всё через var(--…).

import { defaultKeymap, history, historyKeymap } from '@codemirror/commands'
import { markdown, markdownLanguage } from '@codemirror/lang-markdown'
import { HighlightStyle, syntaxHighlighting } from '@codemirror/language'
import { EditorState, type Extension } from '@codemirror/state'
import { EditorView, keymap, placeholder } from '@codemirror/view'
import { tags as t } from '@lezer/highlight'

const theme = EditorView.theme({
  '&': {
    color: 'var(--fg)',
    backgroundColor: 'transparent',
    fontSize: '14.5px',
    height: '100%',
  },
  '.cm-content': {
    fontFamily: 'var(--f-mono)',
    lineHeight: '1.75',
    padding: '0',
    caretColor: 'var(--accent)',
  },
  '.cm-scroller': { fontFamily: 'var(--f-mono)', overflow: 'auto' },
  '&.cm-focused': { outline: 'none' },
  '.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--accent)', borderLeftWidth: '2px' },
  '.cm-selectionBackground, &.cm-focused .cm-selectionBackground, ::selection': {
    backgroundColor: 'var(--accent-bg)',
  },
  '.cm-activeLine': { backgroundColor: 'transparent' },
  '.cm-gutters': { display: 'none' },
  '.cm-line': { padding: '0' },
})

// Подсветка markdown в той же палитре: заголовки — цветом текста и жирным,
// разметка приглушена, ссылки и код — акцентом.
const highlight = HighlightStyle.define([
  { tag: t.heading1, color: 'var(--fg)', fontWeight: '600', fontSize: '1.35em' },
  { tag: t.heading2, color: 'var(--fg)', fontWeight: '600', fontSize: '1.18em' },
  { tag: [t.heading3, t.heading4, t.heading5, t.heading6], color: 'var(--fg)', fontWeight: '600' },
  { tag: t.strong, color: 'var(--fg)', fontWeight: '600' },
  { tag: t.emphasis, color: 'var(--fg)', fontStyle: 'italic' },
  { tag: t.strikethrough, textDecoration: 'line-through', color: 'var(--fg-faint)' },
  { tag: [t.link, t.url], color: 'var(--accent)' },
  { tag: [t.monospace], color: 'var(--accent)' },
  { tag: t.quote, color: 'var(--fg-dim)' },
  { tag: [t.processingInstruction, t.meta, t.punctuation], color: 'var(--fg-faint)' },
  { tag: t.list, color: 'var(--fg-faint)' },
  { tag: t.contentSeparator, color: 'var(--line-strong)' },
])

export type EditorHandle = {
  view: EditorView
  getValue: () => string
  setValue: (text: string) => void
  insert: (text: string) => void
  destroy: () => void
}

/**
 * Создаёт редактор в контейнере.
 * onSave вешается на ⌘S, onChange сообщает о несохранённых правках.
 */
export function createEditor(
  parent: HTMLElement,
  doc: string,
  onChange: () => void,
  onSave: () => void,
): EditorHandle {
  const saveKey: Extension = keymap.of([
    {
      key: 'Mod-s',
      preventDefault: true,
      run: () => {
        onSave()
        return true
      },
    },
  ])

  const view = new EditorView({
    parent,
    state: EditorState.create({
      doc,
      extensions: [
        history(),
        // Свой обработчик сохранения идёт первым, иначе браузер
        // перехватит ⌘S и предложит сохранить страницу.
        saveKey,
        keymap.of([...defaultKeymap, ...historyKeymap]),
        markdown({ base: markdownLanguage, codeLanguages: [] }),
        syntaxHighlighting(highlight),
        EditorView.lineWrapping,
        theme,
        placeholder('Пустая заметка'),
        EditorView.updateListener.of((u) => {
          if (u.docChanged) onChange()
        }),
      ],
    }),
  })

  return {
    view,
    getValue: () => view.state.doc.toString(),
    setValue: (text: string) => {
      view.dispatch({
        changes: { from: 0, to: view.state.doc.length, insert: text },
      })
    },
    insert: (text: string) => {
      const { from, to } = view.state.selection.main
      view.dispatch({
        changes: { from, to, insert: text },
        selection: { anchor: from + text.length },
      })
      view.focus()
    },
    destroy: () => view.destroy(),
  }
}
