import { expect, test } from '@playwright/test'
import {
  fetchContent,
  fetchHash,
  openApp,
  openNote,
  press,
  removeNote,
  resetOrder,
  rootNames,
  seed,
  TOKEN,
  uniq,
} from './svod'

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
  await fetch(`http://127.0.0.1:8123/api/v1/files/${base}/точка.png`, {
    method: 'PUT',
    headers: { Authorization: `Bearer ${TOKEN}` },
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

// ───────────────────────── адреса страниц ─────────────────────────

test('адрес меняется при открытии и переживает перезагрузку', async ({ page }) => {
  const path = `${uniq('Адрес')}.md`
  await seed(path, '# Адрес заметки\n\nтело\n')

  await openApp(page)
  await openNote(page, path)

  await expect(page).toHaveURL(new RegExp(`/n/${encodeURIComponent(path)}$`))

  // Главное: перезагрузка возвращает на ту же заметку, а не в пустой экран.
  await page.reload()
  await expect(page.locator('.note h1')).toHaveText('Адрес заметки')
})

test('кнопка «назад» возвращает к предыдущей заметке', async ({ page }) => {
  const base = uniq('История')
  await seed(`${base}/Первая.md`, '# Первая заметка\n')
  await seed(`${base}/Вторая.md`, '# Вторая заметка\n')

  await openApp(page)
  await openNote(page, `${base}/Первая.md`)
  await openNote(page, `${base}/Вторая.md`)
  await expect(page.locator('.note h1')).toHaveText('Вторая заметка')

  await page.goBack()
  await expect(page.locator('.note h1')).toHaveText('Первая заметка')

  await page.goForward()
  await expect(page.locator('.note h1')).toHaveText('Вторая заметка')
})

test('переход по оглавлению кладёт якорь в адрес и переживает перезагрузку', async ({ page }) => {
  const path = `${uniq('Якорь')}.md`
  const filler = Array.from({ length: 40 }, (_, i) => `Строка ${i} для длины.`).join('\n\n')
  await seed(path, `# Якорь\n\n${filler}\n\n## Нужный раздел\n\nвот он\n\n${filler}\n`)

  await openApp(page)
  await openNote(page, path)

  await page.locator('.outline button', { hasText: 'Нужный раздел' }).click()
  await expect(page).toHaveURL(/#/)

  // Прокрутка по оглавлению плавная, поэтому ждём, а не читаем сразу.
  await expect
    .poll(async () => await page.locator('.main').evaluate((el) => el.scrollTop), { timeout: 5000 })
    .toBeGreaterThan(100)

  await page.reload()
  await expect(page.locator('.note h1')).toHaveText('Якорь')
  await expect
    .poll(async () => await page.locator('.main').evaluate((el) => el.scrollTop), { timeout: 5000 })
    .toBeGreaterThan(100)
})

test('прокрутка возвращается на прежнее место без якоря', async ({ page }) => {
  const path = `${uniq('Прокрутка')}.md`
  const filler = Array.from({ length: 120 }, (_, i) => `Абзац номер ${i}.`).join('\n\n')
  await seed(path, `# Прокрутка\n\n${filler}\n`)

  await openApp(page)
  await openNote(page, path)

  // Дожидаемся, пока текст отрисуется: пока страница короткая, задать
  // смещение нельзя — браузер зажмёт его в ноль, и сохранять будет нечего.
  await expect
    .poll(async () => await page.locator('.main').evaluate((el) => el.scrollHeight))
    .toBeGreaterThan(1500)

  await page.locator('.main').evaluate((el) => {
    el.scrollTop = 900
  })
  await expect
    .poll(async () => await page.locator('.main').evaluate((el) => el.scrollTop))
    .toBeGreaterThan(500)

  // Смещение пишется с задержкой в четверть секунды.
  await page.waitForTimeout(500)

  await page.reload()
  await expect(page.locator('.note h1')).toHaveText('Прокрутка')
  await expect
    .poll(async () => await page.locator('.main').evaluate((el) => el.scrollTop), { timeout: 5000 })
    .toBeGreaterThan(500)
})

// ───────────────────────── временные ссылки ─────────────────────────

test('ссылка открывает заметку в чистом браузере без входа', async ({ page, browser }) => {
  const path = `${uniq('Поделиться')}.md`
  await seed(path, '# Показать другу\n\nэто видно по ссылке\n')

  await openApp(page)
  await openNote(page, path)

  await page.locator('.pill', { hasText: 'ПОДЕЛИТЬСЯ' }).click()
  await page.locator('.dialog button', { hasText: 'Создать ссылку' }).click()

  const url = await page.locator('.share-result input').inputValue()
  expect(url).toContain('/s/')

  // Отдельный браузерный контекст: ни куки, ни хранилища от владельца.
  const guest = await browser.newContext({ storageState: { cookies: [], origins: [] } })
  const guestPage = await guest.newPage()
  await guestPage.goto(url)

  await expect(guestPage.locator('.guest-note h1')).toHaveText('Показать другу')
  await expect(guestPage.locator('.md')).toContainText('это видно по ссылке')
  // У гостя нет ни дерева, ни поиска — их просто нет в этой сборке.
  await expect(guestPage.locator('.panel-l')).toHaveCount(0)
  await expect(guestPage.locator('.status')).toHaveCount(0)
  await guest.close()
})

test('отозванная ссылка перестаёт работать', async ({ page, browser }) => {
  const path = `${uniq('Отзыв')}.md`
  await seed(path, '# Отзыв\n\nвременно\n')

  await openApp(page)
  await openNote(page, path)
  await page.locator('.pill', { hasText: 'ПОДЕЛИТЬСЯ' }).click()
  await page.locator('.dialog button', { hasText: 'Создать ссылку' }).click()
  const url = await page.locator('.share-result input').inputValue()

  await page.locator('.share-row button', { hasText: 'Отозвать' }).first().click()
  await expect(page.locator('.share-row')).toHaveCount(0)

  const guest = await browser.newContext({ storageState: { cookies: [], origins: [] } })
  const guestPage = await guest.newPage()
  await guestPage.goto(url)
  await expect(guestPage.locator('.guest-card h1')).toHaveText('Ссылка не работает')
  await guest.close()
})

test('гость по ссылке не достаёт соседнюю заметку', async ({ page, browser }) => {
  const base = uniq('Изоляция')
  await seed(`${base}/Открытая.md`, '# Открытая\n\nможно\n')
  await seed(`${base}/Тайная.md`, '# Тайная\n\nнельзя\n')

  await openApp(page)
  await openNote(page, `${base}/Открытая.md`)
  await page.locator('.pill', { hasText: 'ПОДЕЛИТЬСЯ' }).click()
  await page.locator('.dialog button', { hasText: 'Создать ссылку' }).click()
  const url = await page.locator('.share-result input').inputValue()

  const guest = await browser.newContext({ storageState: { cookies: [], origins: [] } })
  const guestPage = await guest.newPage()
  await guestPage.goto(url)
  await expect(guestPage.locator('.guest-note h1')).toHaveText('Открытая')

  // Убеждаемся, что у гостя действительно нет ключа владельца.
  expect(await guest.cookies()).toHaveLength(0)

  // Обычные ручки для гостя закрыты, даже когда у него есть рабочая ссылка.
  for (const api of ['/api/v1/tree', `/api/v1/note/${encodeURIComponent(base + '/Тайная.md')}`]) {
    const status = await guestPage.evaluate(
      async (u) => (await fetch(u)).status,
      api,
    )
    expect(status).toBe(401)
  }
  await guest.close()
})

// ───────────────────────── вход ─────────────────────────

test('без входа показывается форма, неверный токен отвергается', async ({ browser }) => {
  const ctx = await browser.newContext({ storageState: { cookies: [], origins: [] } })
  const page = await ctx.newPage()
  await page.goto('/')

  await expect(page.locator('.login-card h1')).toHaveText('Свод')
  await expect(page.locator('.panel-l')).toHaveCount(0)

  await page.locator('.login-card input').fill('явно-не-тот-токен')
  await page.locator('.login-card button').click()
  await expect(page.locator('.login-err')).toContainText('не подошёл')
  await expect(page.locator('.login-card')).toBeVisible()

  await ctx.close()
})

test('верный токен пускает и вход запоминается', async ({ browser }) => {
  const path = `${uniq('ПослеВхода')}.md`
  await seed(path, '# После входа\n')

  const ctx = await browser.newContext({ storageState: { cookies: [], origins: [] } })
  const page = await ctx.newPage()
  await page.goto('/')

  await page.locator('.login-card input').fill(TOKEN)
  await page.locator('.login-card button').click()

  await expect(page.locator('.panel-l .row').first()).toBeVisible()
  await expect(page.locator('.login-card')).toHaveCount(0)

  // Кука выдана, перезагрузка не спрашивает токен заново.
  await page.reload()
  await expect(page.locator('.panel-l .row').first()).toBeVisible()

  await ctx.close()
})

test('гостевая страница отрисована сервером и работает без JavaScript', async ({ page, browser }) => {
  const path = `${uniq('БезСкриптов')}.md`
  await seed(
    path,
    '# Без скриптов\n\nАбзац текста, тег #проверка и `код`.\n\n- [x] сделано\n- [ ] нет\n',
  )

  await openApp(page)
  await openNote(page, path)
  await page.locator('.pill', { hasText: 'ПОДЕЛИТЬСЯ' }).click()
  await page.locator('.dialog button', { hasText: 'Создать ссылку' }).click()
  const url = await page.locator('.share-result input').inputValue()

  // Скрипты выключены: всё, что видно, пришло с сервера готовым.
  const guest = await browser.newContext({
    javaScriptEnabled: false,
    storageState: { cookies: [], origins: [] },
  })
  const guestPage = await guest.newPage()
  await guestPage.goto(url)

  await expect(guestPage.locator('.guest-note h1')).toHaveText('Без скриптов')
  await expect(guestPage.locator('.md')).toContainText('Абзац текста')
  await expect(guestPage.locator('.md .tag')).toHaveText('#проверка')
  await expect(guestPage.locator('.md code')).toHaveText('код')
  await expect(guestPage.locator('.md input[type=checkbox]')).toHaveCount(2)

  await guest.close()
})

test('гостевая страница не тянет разметчик и приложение', async ({ page, browser }) => {
  const path = `${uniq('Лёгкая')}.md`
  await seed(path, '# Лёгкая\n\nтекст\n')

  await openApp(page)
  await openNote(page, path)
  await page.locator('.pill', { hasText: 'ПОДЕЛИТЬСЯ' }).click()
  await page.locator('.dialog button', { hasText: 'Создать ссылку' }).click()
  const url = await page.locator('.share-result input').inputValue()

  const guest = await browser.newContext({ storageState: { cookies: [], origins: [] } })
  const guestPage = await guest.newPage()

  const loaded: string[] = []
  let bytes = 0
  guestPage.on('response', async (r) => {
    loaded.push(new URL(r.url()).pathname)
    try {
      bytes += (await r.body()).length
    } catch {
      // тело могло быть недоступно — на подсчёт это не влияет
    }
  })

  await guestPage.goto(url, { waitUntil: 'networkidle' })
  await expect(guestPage.locator('.guest-note h1')).toHaveText('Лёгкая')

  // Приложение и разметчик гостю не нужны: текст уже отрисован.
  const heavy = loaded.filter((p) => p.includes('main-') || p.includes('editor-'))
  expect(heavy, `гость скачал лишнее: ${heavy.join(', ')}`).toHaveLength(0)

  // Одна страница плюс стили — десятков килобайт тут быть не должно.
  expect(bytes, `скачано ${(bytes / 1024).toFixed(1)} КБ`).toBeLessThan(60 * 1024)

  await guest.close()
})

// ───────────────────────── телефон ─────────────────────────

test.describe('узкий экран', () => {
  test.use({ viewport: { width: 390, height: 844 } })

  test('дерево спрятано, открывается кнопкой и закрывается после выбора', async ({ page }) => {
    const path = `${uniq('Телефон')}.md`
    await seed(path, '# С телефона\n\nтекст\n')

    await page.goto('/')
    // На телефоне дерево не занимает место: текст важнее.
    await expect(page.locator('.panel-l')).not.toBeInViewport()

    await page.locator('.top .pill', { hasText: '☰' }).click()
    await expect(page.locator('.panel-l')).toBeInViewport()

    await page.locator('.palette input').count() // страховка от гонки отрисовки
    await page.locator('.row', { hasText: path.replace('.md', '') }).first().click()

    await expect(page.locator('.note h1')).toHaveText('С телефона')
    // Выбрали заметку — панель ушла, иначе она закрывает текст.
    await expect(page.locator('.panel-l')).not.toBeInViewport()
  })

  test('текст занимает всю ширину и не уезжает вбок', async ({ page }) => {
    const path = `${uniq('Ширина')}.md`
    await seed(
      path,
      '# Ширина\n\n' +
        'Длинный абзац, который обязан переноситься, а не разъезжать страницу вбок. '.repeat(6) +
        '\n\n| Колонка | Ещё колонка | И третья |\n|---|---|---|\n| раз | два | три |\n',
    )

    await page.goto('/')
    await openNote(page, path)

    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
    )
    expect(overflow, 'страница разъезжается вбок').toBeLessThanOrEqual(0)

    const noteWidth = await page.locator('.note').evaluate((el) => el.clientWidth)
    expect(noteWidth).toBeGreaterThan(300)
  })

  test('затемнение закрывает панель по нажатию мимо неё', async ({ page }) => {
    await seed(`${uniq('Затемнение')}.md`, '# Затемнение\n')
    await page.goto('/')

    await page.locator('.top .pill', { hasText: '☰' }).click()
    await expect(page.locator('.backdrop')).toBeVisible()

    await page.locator('.backdrop').click({ position: { x: 350, y: 400 } })
    await expect(page.locator('.panel-l')).not.toBeInViewport()
  })
})

test('гостевая страница читается на телефоне', async ({ page, browser }) => {
  const path = `${uniq('ГостьТелефон')}.md`
  await seed(path, '# Гость с телефона\n\nДлинный текст для проверки переносов. '.repeat(8) + '\n')

  await openApp(page)
  await openNote(page, path)
  await page.locator('.pill', { hasText: 'ПОДЕЛИТЬСЯ' }).click()
  await page.locator('.dialog button', { hasText: 'Создать ссылку' }).click()
  const url = await page.locator('.share-result input').inputValue()

  const guest = await browser.newContext({
    viewport: { width: 390, height: 844 },
    storageState: { cookies: [], origins: [] },
  })
  const guestPage = await guest.newPage()
  await guestPage.goto(url)

  await expect(guestPage.locator('.guest-note h1')).toHaveText('Гость с телефона')
  const overflow = await guestPage.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  )
  expect(overflow, 'гостевая страница разъезжается вбок').toBeLessThanOrEqual(0)

  await guest.close()
})

// ───────────────────────── переименование ─────────────────────────

test('история не обрывается после переименования', async ({ page }) => {
  const base = uniq('Переезд')
  const oldPath = `${base}/Старое.md`
  const newPath = `${base}/Новое.md`

  // Две версии под старым именем.
  const h1 = await seed(oldPath, '# Переезд\n\nпервая версия\n')
  const h2 = await seed(oldPath, '# Переезд\n\nвторая версия\n', h1)

  // Переименование приезжает как удаление плюс создание — так его
  // присылает демон, когда файл переложили в папке.
  await removeNote(oldPath, h2)
  await seed(newPath, '# Переезд\n\nвторая версия\n')

  await openApp(page)
  await openNote(page, newPath)

  // История должна тянуться из-под прежнего имени, иначе заметка
  // выглядела бы только что созданной.
  await expect(page.locator('.history div')).not.toHaveCount(1)
  await expect(page.locator('.history .was').first()).toContainText('Старое')
})

// ───────────────────────── порядок папок ─────────────────────────

test.describe('перетаскивание корневых папок', () => {
  // Порядок общий на весь свод, поэтому тесты идут по одному
  // и начинают с чистого листа.
  test.describe.configure({ mode: 'serial' })

  const a = 'ААА-первая'
  const b = 'БББ-вторая'
  const c = 'ВВВ-третья'

  test.beforeAll(async () => {
    await seed(`${a}/файл.md`, '# Первая\n')
    await seed(`${b}/файл.md`, '# Вторая\n')
    await seed(`${c}/файл.md`, '# Третья\n')
  })

  test.beforeEach(async () => {
    await resetOrder()
  })

  test.afterAll(async () => {
    await resetOrder()
  })

  test('без заданного порядка папки идут по алфавиту', async ({ page }) => {
    await openApp(page)
    const names = await rootNames(page)
    expect(names.indexOf(a)).toBeLessThan(names.indexOf(b))
    expect(names.indexOf(b)).toBeLessThan(names.indexOf(c))
  })

  test('перетаскивание меняет порядок и переживает перезагрузку', async ({ page }) => {
    await openApp(page)

    const third = page.locator('.panel-l .row', { hasText: c })
    const first = page.locator('.panel-l .row', { hasText: a })
    await third.dragTo(first)

    // Третья должна оказаться выше первой.
    await expect
      .poll(async () => {
        const names = await rootNames(page)
        return names.indexOf(c) < names.indexOf(a)
      })
      .toBe(true)

    // Порядок хранится на сервере, поэтому переживает перезагрузку —
    // и переедет на другое устройство.
    await page.reload()
    await expect(page.locator('.panel-l .row').first()).toBeVisible()
    const names = await rootNames(page)
    expect(names.indexOf(c)).toBeLessThan(names.indexOf(a))
  })

  test('порядок виден в другом браузере: он хранится не локально', async ({ page, browser }) => {
    await openApp(page)
    await page.locator('.panel-l .row', { hasText: c }).dragTo(
      page.locator('.panel-l .row', { hasText: a }),
    )
    await expect
      .poll(async () => (await rootNames(page)).indexOf(c) < (await rootNames(page)).indexOf(a))
      .toBe(true)

    // Другой контекст — своё хранилище браузера, но тот же сервер.
    const other = await browser.newContext({ storageState: 'e2e/.auth.json' })
    const otherPage = await other.newPage()
    await otherPage.goto('/')
    await expect(otherPage.locator('.panel-l .row').first()).toBeVisible()

    const names = await rootNames(otherPage)
    expect(names.indexOf(c)).toBeLessThan(names.indexOf(a))
    await other.close()
  })

  test('вложенные папки и файлы перетаскивать нельзя', async ({ page }) => {
    await openApp(page)

    // Корневая папка — можно.
    await expect(page.locator('.panel-l .row', { hasText: a }).first()).toHaveAttribute(
      'draggable',
      'true',
    )

    // Разворачиваем и смотрим на вложенное.
    await page.locator('.panel-l .row', { hasText: a }).first().click()
    const child = page.locator('.panel-l .row', { hasText: 'файл' }).first()
    await expect(child).toBeVisible()
    await expect(child).toHaveAttribute('draggable', 'false')
  })

  test('новая папка не теряется: встаёт после заданных', async ({ page }) => {
    await openApp(page)
    await page.locator('.panel-l .row', { hasText: c }).dragTo(
      page.locator('.panel-l .row', { hasText: a }),
    )
    await expect.poll(async () => (await rootNames(page)).indexOf(c) === 0).toBeTruthy()

    // Папка, которой не было в момент задания порядка.
    const fresh = `ЯЯЯ-${uniq('новая')}`
    await seed(`${fresh}/файл.md`, '# Новая\n')

    await expect.poll(async () => (await rootNames(page)).includes(fresh)).toBe(true)
    const names = await rootNames(page)
    // Заданные — впереди, новая — среди неупорядоченных, но в дереве есть.
    expect(names.indexOf(c)).toBeLessThan(names.indexOf(fresh))
  })
})
