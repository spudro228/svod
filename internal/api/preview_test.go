package api_test

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"
)

// Карточка ссылки для мессенджеров. Краулеры не выполняют JavaScript,
// поэтому всё, что они увидят, должно лежать в готовом HTML.

func fetchPage(t *testing.T, url string) string {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestКарточкаСодержитЗаголовокИОтрывок(t *testing.T) {
	srv, owner := newServer(t, token)
	login(t, srv.URL, owner)
	put(t, srv.URL, owner, "Превью.md",
		"---\ntags: [демо]\n---\n\n# Заголовок для карточки\n\n"+
			"Первый абзац, который должен попасть в превью телеграма "+
			"и рассказать, о чём заметка, ещё до перехода по ссылке. "+
			"Дальше идёт длинный хвост, который в карточку не влезет "+
			"и должен быть обрезан по границе слова, а не посередине.\n")

	key, _ := share(t, srv.URL, owner, "Превью.md", 24)
	page := fetchPage(t, srv.URL+"/s/"+key)

	if !strings.Contains(page, `property="og:title" content="Заголовок для карточки"`) {
		t.Error("нет заголовка в карточке")
	}
	if !strings.Contains(page, `property="og:description"`) {
		t.Error("нет отрывка в карточке")
	}
	if !strings.Contains(page, "Первый абзац") {
		t.Error("отрывок не из текста заметки")
	}
	if !strings.Contains(page, "Читать далее") {
		t.Error("нет предложения читать дальше")
	}
	// Разметка и служебное в карточку попадать не должны.
	if strings.Contains(page, `content="---`) || strings.Contains(page, "# Заголовок") {
		t.Error("в отрывок попала разметка")
	}
	if !strings.Contains(page, `property="og:url"`) {
		t.Error("нет канонического адреса")
	}

	// Заголовок должен быть один: браузер берёт первый встреченный,
	// и лишний из сборки перебил бы название заметки.
	if n := strings.Count(page, "<title>"); n != 1 {
		t.Errorf("тегов title в странице: %d, ожидался один", n)
	}
	if !strings.Contains(page, "<title>Заголовок для карточки — Свод</title>") {
		t.Error("во вкладке не название заметки")
	}
}

// Содержимое заметок идёт прямо в HTML — экранирование обязательно.
func TestКарточкаЭкранируетСодержимое(t *testing.T) {
	srv, owner := newServer(t, token)
	login(t, srv.URL, owner)
	put(t, srv.URL, owner, "Опасная.md",
		"# Кавычка \" и <script>alert(1)</script>\n\nТекст с \"кавычками\" и <b>тегами</b>.\n")

	key, _ := share(t, srv.URL, owner, "Опасная.md", 24)
	page := fetchPage(t, srv.URL+"/s/"+key)

	// Разметку goldmark отбрасывает ещё при разборе, но это не защита:
	// проверяем, что в страницу не попало ничего исполняемого.
	if strings.Contains(page, "<script>") || strings.Contains(page, "</script>") {
		t.Fatal("содержимое заметки попало в HTML как разметка")
	}

	// Главное — кавычка. Неэкранированная разорвала бы атрибут
	// и позволила бы дописать в тег что угодно.
	if !strings.Contains(page, "&#34;") {
		t.Error("кавычка из заметки не экранирована")
	}
	if !strings.Contains(page, "&lt;") {
		t.Error("угловая скобка из текста не экранирована")
	}
}

// По разнице ответов нельзя выяснять, существовал ли ключ.
func TestНедействительнаяСсылкаНеРаскрываетКарточку(t *testing.T) {
	srv, owner := newServer(t, token)
	login(t, srv.URL, owner)
	put(t, srv.URL, owner, "Секрет.md", "# Секретный заголовок\n\nтайна\n")

	key, _ := share(t, srv.URL, owner, "Секрет.md", 24)
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/share/"+key, nil)
	if res, err := owner.Do(req); err == nil {
		res.Body.Close()
	}

	revoked := fetchPage(t, srv.URL+"/s/"+key)
	if strings.Contains(revoked, "Секретный заголовок") {
		t.Fatal("отозванная ссылка всё ещё показывает заголовок в карточке")
	}

	missing := fetchPage(t, srv.URL+"/s/выдуманный-ключ")
	if len(revoked) != len(missing) {
		t.Errorf("отозванная и несуществующая отдают разные страницы: %d и %d байт",
			len(revoked), len(missing))
	}
}

func TestКартинкаЗаметкиПопадаетВКарточку(t *testing.T) {
	srv, owner := newServer(t, token)
	login(t, srv.URL, owner)
	put(t, srv.URL, owner, "Вложения/снимок.png", "не настоящий png, но неважно")
	put(t, srv.URL, owner, "СКартинкой.md",
		"# С картинкой\n\nВот она: ![[Вложения/снимок.png]]\n")

	key, _ := share(t, srv.URL, owner, "СКартинкой.md", 24)
	page := fetchPage(t, srv.URL+"/s/"+key)

	if !strings.Contains(page, `property="og:image"`) {
		t.Fatal("картинка не попала в карточку")
	}
	if !strings.Contains(page, "/api/v1/shared/"+key+"/asset/") {
		t.Error("адрес картинки ведёт мимо временной ссылки")
	}
	if !strings.Contains(page, `name="twitter:card" content="summary_large_image"`) {
		t.Error("с картинкой ожидалась крупная карточка")
	}
}

// Мелкие картинки Telegram в карточку не берёт, но место под обложку
// при этом занимает. Такие лучше не предлагать вовсе.
func TestМелкаяКартинкаНеСтановитсяОбложкой(t *testing.T) {
	srv, owner := newServer(t, token)
	login(t, srv.URL, owner)

	put(t, srv.URL, owner, "Вложения/крошка.png", string(makePNG(t, 16, 16)))
	put(t, srv.URL, owner, "Мелкая.md", "# Мелкая\n\n![[Вложения/крошка.png]]\n")

	key, _ := share(t, srv.URL, owner, "Мелкая.md", 24)
	page := fetchPage(t, srv.URL+"/s/"+key)

	if strings.Contains(page, `property="og:image"`) {
		t.Error("крошечная картинка предложена как обложка")
	}
	if !strings.Contains(page, `name="twitter:card" content="summary"`) {
		t.Error("без обложки ожидалась обычная карточка")
	}
}

func TestКрупнаяКартинкаОтдаётсяСРазмерами(t *testing.T) {
	srv, owner := newServer(t, token)
	login(t, srv.URL, owner)

	put(t, srv.URL, owner, "Вложения/обложка.png", string(makePNG(t, 640, 360)))
	put(t, srv.URL, owner, "Крупная.md", "# Крупная\n\n![[Вложения/обложка.png]]\n")

	key, _ := share(t, srv.URL, owner, "Крупная.md", 24)
	page := fetchPage(t, srv.URL+"/s/"+key)

	if !strings.Contains(page, `property="og:image"`) {
		t.Fatal("обложка не попала в карточку")
	}
	// Размеры избавляют Telegram от необходимости скачивать файл,
	// чтобы решить, какую карточку строить.
	if !strings.Contains(page, `property="og:image:width" content="640"`) {
		t.Error("нет ширины обложки")
	}
	if !strings.Contains(page, `property="og:image:height" content="360"`) {
		t.Error("нет высоты обложки")
	}
	if !strings.Contains(page, `property="og:image:type" content="image/png"`) {
		t.Error("нет типа обложки")
	}
	if !strings.Contains(page, `name="twitter:card" content="summary_large_image"`) {
		t.Error("с обложкой ожидалась крупная карточка")
	}
}

func makePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: 0x4F, G: 0xB3, B: 0xBF, A: 0xFF})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
