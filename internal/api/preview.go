package api

import (
	"html"
	"io/fs"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"github.com/spudro228/svod/internal/index"
)

// Карточка ссылки для мессенджеров и соцсетей.
//
// Краулеры Telegram, Slack и прочих не выполняют JavaScript: гостевая
// страница — обычное SPA, и бот увидел бы пустой div. Поэтому теги
// Open Graph подставляются здесь, в готовый HTML, до отдачи.

const excerptLimit = 180

var (
	reFrontmatter = regexp.MustCompile(`(?s)\A\x{FEFF}?---\r?\n.*?\r?\n---[ \t]*\r?\n?`)
	reFence       = regexp.MustCompile("(?s)```.*?```")
	reHeading     = regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s+.*$`)
	reWikiEmbed   = regexp.MustCompile(`!\[\[[^\]]*\]\]`)
	reWikiLink    = regexp.MustCompile(`\[\[([^\[\]|]+)(?:\|([^\[\]]*))?\]\]`)
	reMdImage     = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	reMdLink      = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	// Маркеры цитат убираем только в начале строки: в прозе «A > B»
	// встречается, и вырезать оттуда знак больше — портить текст.
	reQuote   = regexp.MustCompile(`(?m)^\s{0,3}>+\s?`)
	reMarkers = regexp.MustCompile("[*_`]+")
	reSpaces  = regexp.MustCompile(`\s+`)
)

// sharePreview отдаёт гостевую страницу с карточкой ссылки в шапке.
func (s *Server) sharePreview(w http.ResponseWriter, r *http.Request, webFS fs.FS) bool {
	page, err := fs.ReadFile(webFS, "share.html")
	if err != nil {
		return false
	}

	key := strings.TrimPrefix(r.URL.Path, "/s/")
	if i := strings.IndexByte(key, '/'); i >= 0 {
		key = key[:i]
	}

	meta := ""
	if sh, _, err := s.st.Share(key); err == nil {
		if note, err := s.st.Note(sh.Path); err == nil {
			meta = ogTags(note.Title, excerpt(note.Content), shareURL(r, key),
				previewImage(r, key, note.Content))
		}
	}
	// Недействительная ссылка отдаёт страницу без карточки: краулер
	// не должен по разнице ответов узнавать, существовал ли ключ.

	noIndex(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(strings.Replace(string(page), "</head>", meta+"</head>", 1)))
	return true
}

// ogTags собирает шапку карточки. Всё пользовательское экранируется:
// сюда попадает содержимое заметок.
func ogTags(title, desc, url, image string) string {
	if title == "" {
		title = "Заметка"
	}
	var b strings.Builder
	tag := func(property, content string) {
		if content == "" {
			return
		}
		b.WriteString(`<meta property="` + property + `" content="` + html.EscapeString(content) + `">` + "\n")
	}
	name := func(n, content string) {
		if content == "" {
			return
		}
		b.WriteString(`<meta name="` + n + `" content="` + html.EscapeString(content) + `">` + "\n")
	}

	b.WriteString("<title>" + html.EscapeString(title) + " — Свод</title>\n")
	name("description", desc)

	tag("og:type", "article")
	tag("og:site_name", "Свод")
	tag("og:title", title)
	tag("og:description", desc)
	tag("og:url", url)
	tag("og:locale", "ru_RU")
	tag("og:image", image)

	// Без карточки Twitter превью в некоторых клиентах остаётся голой ссылкой.
	if image != "" {
		name("twitter:card", "summary_large_image")
		name("twitter:image", image)
	} else {
		name("twitter:card", "summary")
	}
	name("twitter:title", title)
	name("twitter:description", desc)

	return b.String()
}

// excerpt делает из markdown человеческую строку для карточки:
// без разметки, без заголовков, без кода, обрезанную по границе слова.
func excerpt(content string) string {
	t := reFrontmatter.ReplaceAllString(content, "")
	t = reFence.ReplaceAllString(t, " ")
	t = reHeading.ReplaceAllString(t, " ")
	t = reWikiEmbed.ReplaceAllString(t, " ")
	t = reMdImage.ReplaceAllString(t, " ")

	// У ссылок оставляем подпись, адрес выбрасываем.
	t = reWikiLink.ReplaceAllStringFunc(t, func(m string) string {
		parts := reWikiLink.FindStringSubmatch(m)
		if parts[2] != "" {
			return parts[2]
		}
		return parts[1]
	})
	t = reMdLink.ReplaceAllString(t, "$1")

	t = reQuote.ReplaceAllString(t, "")
	t = reMarkers.ReplaceAllString(t, "")
	t = reSpaces.ReplaceAllString(t, " ")
	t = strings.TrimSpace(t)

	runes := []rune(t)
	if len(runes) <= excerptLimit {
		return t
	}

	// Режем по последнему пробелу, чтобы не обрывать слово посередине.
	cut := runes[:excerptLimit]
	for i := len(cut) - 1; i > excerptLimit/2; i-- {
		if unicode.IsSpace(cut[i]) {
			cut = cut[:i]
			break
		}
	}
	return strings.TrimRight(strings.TrimSpace(string(cut)), ".,;:—-") + "… Читать далее"
}

// previewImage подставляет в карточку первую картинку заметки, если она есть.
// Адрес ведёт через ту же ссылку, поэтому доступен без токена — но только
// он, остальные вложения по-прежнему закрыты.
func previewImage(r *http.Request, key, content string) string {
	parsed := index.Parse([]byte(content))
	for _, e := range parsed.Embeds {
		if !isPreviewable(e) {
			continue
		}
		scheme := "http"
		if isHTTPS(r) {
			scheme = "https"
		}
		segments := strings.Split(e, "/")
		for i, seg := range segments {
			segments[i] = urlEscape(seg)
		}
		return scheme + "://" + r.Host + "/api/v1/shared/" + key + "/asset/" + strings.Join(segments, "/")
	}
	return ""
}

// urlEscape кодирует один сегмент пути: кириллица и пробелы в адресе
// карточки должны быть в процентах, иначе краулер их не разберёт.
func urlEscape(seg string) string {
	return url.PathEscape(seg)
}

func isPreviewable(p string) bool {
	lower := strings.ToLower(p)
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".gif", ".webp"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}
