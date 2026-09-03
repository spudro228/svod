// Единственный скрипт на гостевой странице: набор формул.
//
// Всё остальное сервер уже отрисовал. Этот файл подключается только тогда,
// когда в заметке действительно есть формулы, и подтягивает KaTeX лениво.

async function typeset() {
  const spots = document.querySelectorAll<HTMLElement>('.math-src')
  if (spots.length === 0) return

  const [{ default: katex }] = await Promise.all([
    import('katex'),
    import('katex/dist/katex.min.css'),
  ])

  spots.forEach((el) => {
    const tex = el.dataset.tex
    if (!tex) return
    try {
      el.innerHTML = katex.renderToString(tex, {
        displayMode: el.classList.contains('math-display'),
        throwOnError: false,
        strict: false,
        output: 'html',
      })
      el.classList.remove('math-src')
    } catch {
      // Битую формулу оставляем исходником: потерять её хуже.
    }
  })
}

void typeset()
