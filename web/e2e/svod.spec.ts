import { expect, test } from '@playwright/test'
import { fetchContent, fetchHash, openApp, openNote, press, seed, uniq } from './svod'

// ───────────────────────── чтение ─────────────────────────

test('дерево показывает залитые заметки, клик открывает', async ({ page }) => {
  const name = uniq('Чтение')
  await seed(`${name}/Внутри.md`, '# Внутри папки\n\nтекст заметки\n')

  await openApp(page)

  await page.locator('.row', { hasText: name }).first().click()
  await page.locator('.row', { hasText: 'Внутри' }).first().click()

  await expect(page.locator('.note h1')).toHaveText('Внутри папки')
  await expect(page.locator('.md')).toContainText('текст заметки')
})

test('markdown отрисован: заголовки, код, задачи, теги', async ({ page }) => {
  const path = `${uniq('Разметка')}.md`
  await seed(
    path,
    '# Разметка\n\n## Раздел\n\nСтрока с `кодом` и тегом #проверка.\n\n' +
      '- [x] сделано\n- [ ] не сделано\n',
  )

  await openApp(page)
  await openNote(page, path)

  await expect(page.locator('.md h2')).toHaveText('Раздел')
  await expect(page.locator('.md code')).toHaveText('кодом')
  await expect(page.locator('.md .tag')).toHaveText('#проверка')
  await expect(page.locator('.md li.task')).toHaveCount(2)
  await expect(page.locator('.md li.task.done')).toHaveCount(1)
})

test('wiki-ссылка кликается и уводит в соседнюю заметку', async ({ page }) => {
  const base = uniq('Связи')
  await seed(`${base}/Цель.md`, '# Цель\n\nсюда ведёт ссылка\n')
  await seed(`${base}/Источник.md`, `# Источник\n\nСсылка на [[${base}/Цель]].\n`)

  await openApp(page)
  await openNote(page, `${base}/Источник.md`)

  await page.locator('.md .wikilink').click()
  await expect(page.locator('.note h1')).toHaveText('Цель')
})

test('бэклинки и оглавление собираются', async ({ page }) => {
  const base = uniq('Панель')
  await seed(`${base}/Цель.md`, '# Цель\n\n## Первый\n\ntext\n\n## Второй\n\ntext\n')
  await seed(`${base}/Ссылается.md`, `# Ссылается\n\n[[${base}/Цель]]\n`)

  await openApp(page)
  await openNote(page, `${base}/Цель.md`)

  await expect(page.locator('.outline button')).toHaveCount(3) // H1 + два H2
  await expect(page.locator('.panel-r')).toContainText('Ссылки сюда · 1')
  await expect(page.locator('.links button')).toHaveText('Ссылается')
})

// ───────────────────────── навигация ─────────────────────────

test('⌘K находит заметку по имени', async ({ page }) => {
  const path = `${uniq('Переход')}.md`
  await seed(path, '# Переход работает\n')

  await openApp(page)
  await press(page, 'k')
  await page.locator('.palette input').fill(path.replace('.md', ''))
  await page.keyboard.press('Enter')

  await expect(page.locator('.note h1')).toHaveText('Переход работает')
})

test('⌘⇧F ищет по содержимому', async ({ page }) => {
  const word = uniq('загогулина').replace(/-/g, '')
  const path = `${uniq('Поиск')}.md`
  await seed(path, `# Заметка про поиск\n\nВнутри спрятано слово ${word}.\n`)

  await openApp(page)
  await press(page, 'f', true)
  await page.locator('.palette input').fill(word)

  await expect(page.locator('.hit').first()).toContainText('Заметка про поиск')
  await page.keyboard.press('Enter')
  await expect(page.locator('.note h1')).toHaveText('Заметка про поиск')
})

test('панели сворачиваются, тема переключается', async ({ page }) => {
  await seed(`${uniq('Панели')}.md`, '# Панели\n')
  await openApp(page)

  await expect(page.locator('.panel-l')).toBeVisible()
  await press(page, '\\')
  await expect(page.locator('.panel-l')).toBeHidden()
  await press(page, '\\')
  await expect(page.locator('.panel-l')).toBeVisible()

  const html = page.locator('html')
  await expect(html).toHaveAttribute('data-theme', 'dark')
  await press(page, 'l', true)
  await expect(html).toHaveAttribute('data-theme', 'light')
})

// ───────────────────────── правка ─────────────────────────

test('⌘E открывает редактор с текстом заметки', async ({ page }) => {
  const path = `${uniq('Редактор')}.md`
  await seed(path, '# Редактор\n\nисходный текст\n')

  await openApp(page)
  await openNote(page, path)

  await press(page, 'e')
  await expect(page.locator('.cm-editor')).toBeVisible()
  await expect(page.locator('.cm-content')).toContainText('исходный текст')
  await expect(page.locator('.pill.is-on')).toHaveText('ПРАВКА')
})

test('правка сохраняется по ⌘S и переживает перезагрузку', async ({ page }) => {
  const path = `${uniq('Сохранение')}.md`
  await seed(path, '# Сохранение\n\nстарый текст\n')

  await openApp(page)
  await openNote(page, path)
  await press(page, 'e')
  await expect(page.locator('.cm-content')).toContainText('старый текст')

  await page.locator('.cm-content').click()
  await page.keyboard.press('End')
  await page.keyboard.type(' ДОПИСАНО')

  await expect(page.locator('.unsaved')).toBeVisible()
  await press(page, 's')

  // Полоса «не сохранено» должна погаснуть, конфликта быть не должно.
  await expect(page.locator('.unsaved')).toBeHidden()
  await expect(page.locator('.conflict-bar')).toHaveCount(0)

  // Главное: текст действительно лежит на сервере.
  await expect
    .poll(async () => await fetchContent(path), { timeout: 7_000 })
    .toContain('ДОПИСАНО')

  // И переживает перезагрузку страницы.
  await page.reload()
  await openNote(page, path)
  await expect(page.locator('.md')).toContainText('ДОПИСАНО')
})

test('выход из режима правки сохраняет текст сам', async ({ page }) => {
  const path = `${uniq('ВыходИзПравки')}.md`
  await seed(path, '# Выход\n\nбыло\n')

  await openApp(page)
  await openNote(page, path)
  await press(page, 'e')
  await page.locator('.cm-content').click()
  await page.keyboard.press('End')
  await page.keyboard.type(' СТАЛО')

  // Уходим в чтение, не нажимая ⌘S.
  await press(page, 'e')
  await expect(page.locator('.md')).toBeVisible()

  await expect
    .poll(async () => await fetchContent(path), { timeout: 7_000 })
    .toContain('СТАЛО')
})

test('конфликт показывает полосу и сохраняет текст копией', async ({ page }) => {
  const path = `${uniq('Конфликт')}.md`
  const hash = await seed(path, '# Конфликт\n\nобщее начало\n')

  await openApp(page)
  await openNote(page, path)
  await press(page, 'e')
  await expect(page.locator('.cm-content')).toContainText('общее начало')

  // Пока вкладка открыта, кто-то другой правит тот же файл.
  await seed(path, '# Конфликт\n\nправка с другой машины\n', hash)

  await page.locator('.cm-content').click()
  await page.keyboard.press('End')
  await page.keyboard.type(' МОЯ ВЕРСИЯ')
  await press(page, 's')

  // Сервер отказал, но текст не потерян — есть выбор.
  await expect(page.locator('.conflict-bar')).toBeVisible()
  await expect(await fetchContent(path)).toContain('правка с другой машины')

  await page.locator('.conflict-actions button', { hasText: 'копией' }).click()

  // Своя версия уехала отдельным файлом, чужая цела.
  await expect(page.locator('.crumb b')).toContainText('конфликт')
  await expect(page.locator('.md')).toContainText('МОЯ ВЕРСИЯ')
  await expect(await fetchContent(path)).toContain('правка с другой машины')
})

// ───────────────────────── живые обновления ─────────────────────────

test('чужая правка приезжает во вкладку без перезагрузки', async ({ page }) => {
  const path = `${uniq('Живое')}.md`
  const hash = await seed(path, '# Живое\n\nпервая версия\n')

  await openApp(page)
  await openNote(page, path)
  await expect(page.locator('.md')).toContainText('первая версия')

  await seed(path, '# Живое\n\nвторая версия приехала\n', hash)

  await expect(page.locator('.md')).toContainText('вторая версия приехала')
})

// ───────────────────────── вложения ─────────────────────────

test('картинка показывается в тексте и своей карточкой', async ({ page }) => {
  const base = uniq('Вложение')
  // Однопиксельный png, чтобы не тащить бинарь в репозиторий.
  const png = Buffer.from(
    'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==',
    'base64',
  )
  await fetch(`${'http://127.0.0.1:8123'}/api/v1/files/${base}/точка.png`, {
    method: 'PUT',
    body: png,
  })
  await seed(`${base}/Смотри.md`, `# Смотри\n\nВот картинка: ![[${base}/точка.png]]\n`)

  await openApp(page)
  await openNote(page, `${base}/Смотри.md`)

  const img = page.locator('.md img.embed')
  await expect(img).toBeVisible()
  await expect(img).toHaveAttribute('src', /raw/)
  // Битая картинка имела бы нулевую ширину.
  expect(await img.evaluate((el: HTMLImageElement) => el.naturalWidth)).toBeGreaterThan(0)
})

// ───────────────────────── состояние ─────────────────────────

test('статус-бар показывает связь и число файлов', async ({ page }) => {
  await seed(`${uniq('Статус')}.md`, '# Статус\n')
  await openApp(page)

  await expect(page.locator('.status .dot.ok')).toBeVisible()
  await expect(page.locator('.status')).toContainText('синхронизировано')
  await expect(page.locator('.status')).toContainText('файлов')
})

test('история версий растёт после правки', async ({ page }) => {
  const path = `${uniq('История')}.md`
  const h1 = await seed(path, '# История\n\nверсия один\n')
  await seed(path, '# История\n\nверсия два\n', h1)

  await openApp(page)
  await openNote(page, path)

  await expect(page.locator('.history div')).toHaveCount(2)
  await expect(page.locator('.history')).toContainText('тест')
  expect(await fetchHash(path)).toHaveLength(64)
})

// ───────────────────────── формулы ─────────────────────────

test('формулы LaTeX набираются, а не остаются исходником', async ({ page }) => {
  const path = `${uniq('Формулы')}.md`
  await seed(
    path,
    '# Формулы\n\nНеравенство треугольника $|a + b| \\leq |a| + |b|$ и предел:\n\n' +
      '$$\\lim_{n \\to \\infty} \\sqrt[n]{n} = 1$$\n',
  )

  await openApp(page)
  await openNote(page, path)

  // KaTeX подтягивается лениво, поэтому ждём появления его разметки.
  await expect(page.locator('.md .katex').first()).toBeVisible()
  await expect(page.locator('.md .math-block .katex')).toHaveCount(1)

  // Исходников на странице остаться не должно.
  await expect(page.locator('.md .math-raw')).toHaveCount(0)
  await expect(page.locator('.md')).not.toContainText('\\leq')
})

test('доллары в обычном тексте формулами не становятся', async ({ page }) => {
  const path = `${uniq('Деньги')}.md`
  await seed(
    path,
    '# Деньги и код\n\nПодписка стоит $5 в месяц, а годовая $50.\n\n' +
      '```php\n$service->doWork($id, $name);\n```\n\nИ переменная `$HOME` в тексте.\n',
  )

  await openApp(page)
  await openNote(page, path)

  await expect(page.locator('.md .katex')).toHaveCount(0)
  await expect(page.locator('.md')).toContainText('Подписка стоит $5 в месяц, а годовая $50.')
  await expect(page.locator('.md pre code')).toContainText('$service->doWork($id, $name);')
})
