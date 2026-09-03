// Package render превращает markdown в HTML на сервере.
//
// Нужен для гостевых страниц: там браузеру незачем скачивать сто килобайт
// разметчика и делать второй запрос за текстом, который сервер уже знает.
// Страница приходит готовой за один круг.
//
// Свой синтаксис разобран парсерами goldmark, а не заменами по готовому
// HTML: парсеры не заглядывают внутрь блоков кода, поэтому [[ссылка]]
// в примере кода останется текстом.
package render

import (
	"bytes"
	"html"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Assets отдаёт адрес вложения по цели из текста заметки.
// Пустая строка означает, что вложение недоступно.
type Assets func(target string) string

// Options — то, что рендереру нужно знать о месте показа.
type Options struct {
	// Asset превращает цель вложения в адрес. Для гостевой страницы
	// он ведёт через временную ссылку, поэтому чужие файлы недоступны.
	Asset Assets
}

// ───────────────────────── свои узлы ─────────────────────────

type wikiNode struct {
	ast.BaseInline
	Target string
	Label  string
	Embed  bool
}

var kindWiki = ast.NewNodeKind("SvodWiki")

func (n *wikiNode) Kind() ast.NodeKind         { return kindWiki }
func (n *wikiNode) Dump(src []byte, level int) { ast.DumpHelper(n, src, level, nil, nil) }

type tagNode struct {
	ast.BaseInline
	Tag string
}

var kindTag = ast.NewNodeKind("SvodTag")

func (n *tagNode) Kind() ast.NodeKind         { return kindTag }
func (n *tagNode) Dump(src []byte, level int) { ast.DumpHelper(n, src, level, nil, nil) }

type mathNode struct {
	ast.BaseInline
	TeX     string
	Display bool
}

var kindMath = ast.NewNodeKind("SvodMath")

func (n *mathNode) Kind() ast.NodeKind         { return kindMath }
func (n *mathNode) Dump(src []byte, level int) { ast.DumpHelper(n, src, level, nil, nil) }

// ───────────────────────── парсеры ─────────────────────────

type wikiParser struct{}

func (wikiParser) Trigger() []byte { return []byte{'[', '!'} }

func (wikiParser) Parse(_ ast.Node, block text.Reader, _ parser.Context) ast.Node {
	line, _ := block.PeekLine()
	embed := false
	rest := line

	if len(rest) > 0 && rest[0] == '!' {
		embed = true
		rest = rest[1:]
	}
	if len(rest) < 4 || rest[0] != '[' || rest[1] != '[' {
		return nil
	}
	end := bytes.Index(rest, []byte("]]"))
	if end < 2 {
		return nil
	}
	inner := string(rest[2:end])
	if strings.ContainsAny(inner, "[\n") || strings.TrimSpace(inner) == "" {
		return nil
	}

	target, label := inner, inner
	if bar := strings.Index(inner, "|"); bar >= 0 {
		target = strings.TrimSpace(inner[:bar])
		label = strings.TrimSpace(inner[bar+1:])
		if label == "" {
			label = target
		}
	} else {
		target = strings.TrimSpace(inner)
		label = target
	}

	consumed := end + 2
	if embed {
		consumed++
	}
	block.Advance(consumed)
	return &wikiNode{Target: target, Label: label, Embed: embed}
}

type tagParser struct{}

var reTag = regexp.MustCompile(`^#([\p{L}\d][\p{L}\d_/-]*)`)

func (tagParser) Trigger() []byte { return []byte{'#'} }

func (tagParser) Parse(_ ast.Node, block text.Reader, _ parser.Context) ast.Node {
	line, seg := block.PeekLine()
	// Тег начинается либо со строки, либо после пробела: иначе «C#» в тексте
	// превратилось бы в тег.
	if seg.Start > 0 {
		prev := block.Source()[seg.Start-1]
		if !unicode.IsSpace(rune(prev)) && prev != '(' {
			return nil
		}
	}
	m := reTag.FindSubmatch(line)
	if m == nil {
		return nil
	}
	block.Advance(len(m[0]))
	return &tagNode{Tag: string(m[1])}
}

type mathParser struct{}

func (mathParser) Trigger() []byte { return []byte{'$'} }

func (mathParser) Parse(_ ast.Node, block text.Reader, _ parser.Context) ast.Node {
	line, _ := block.PeekLine()
	if len(line) < 2 || line[0] != '$' {
		return nil
	}

	display := len(line) > 1 && line[1] == '$'
	marker := "$"
	if display {
		marker = "$$"
	}
	rest := line[len(marker):]
	end := bytes.Index(rest, []byte(marker))
	if end < 1 {
		return nil
	}
	tex := string(rest[:end])
	if strings.TrimSpace(tex) == "" {
		return nil
	}
	if !display {
		// Те же правила, что и в клиенте: «заплатил $5 и $10» формулой
		// становиться не должно.
		if unicode.IsSpace(rune(tex[0])) || unicode.IsSpace(rune(tex[len(tex)-1])) {
			return nil
		}
		after := rest[end+len(marker):]
		if len(after) > 0 && after[0] >= '0' && after[0] <= '9' {
			return nil
		}
	}

	block.Advance(len(marker)*2 + end)
	return &mathNode{TeX: tex, Display: display}
}

// ───────────────────────── отрисовка ─────────────────────────

type svodRenderer struct {
	opts Options
}

func (r *svodRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(kindWiki, r.renderWiki)
	reg.Register(kindTag, r.renderTag)
	reg.Register(kindMath, r.renderMath)
	reg.Register(ast.KindImage, r.renderImage)
}

func (r *svodRenderer) renderWiki(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*wikiNode)

	if n.Embed {
		if src := r.asset(n.Target); src != "" {
			w.WriteString(`<img class="embed" src="` + html.EscapeString(src) +
				`" alt="` + html.EscapeString(n.Label) + `" loading="lazy">`)
		} else {
			w.WriteString(`<span class="missing">нет вложения: ` + html.EscapeString(n.Target) + `</span>`)
		}
		return ast.WalkContinue, nil
	}

	// Гостю переходить некуда: соседние заметки ему недоступны,
	// поэтому ссылка показывается текстом, а не приманкой на клик.
	w.WriteString(`<span class="wikilink is-dead">` + html.EscapeString(n.Label) + `</span>`)
	return ast.WalkContinue, nil
}

func (r *svodRenderer) renderTag(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		w.WriteString(`<span class="tag">#` + html.EscapeString(node.(*tagNode).Tag) + `</span>`)
	}
	return ast.WalkContinue, nil
}

func (r *svodRenderer) renderMath(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*mathNode)
	// Набором занимается KaTeX уже в браузере: годной библиотеки для Go нет.
	// До её загрузки видно исходник, а не пустое место.
	class := "math-src"
	if n.Display {
		class += " math-display"
	}
	w.WriteString(`<span class="` + class + `" data-tex="` + html.EscapeString(n.TeX) + `">` +
		html.EscapeString(n.TeX) + `</span>`)
	return ast.WalkContinue, nil
}

// renderImage переписывает обычные картинки markdown на адреса вложений.
func (r *svodRenderer) renderImage(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.Image)
	dest := string(n.Destination)

	src := dest
	if !strings.HasPrefix(dest, "http://") && !strings.HasPrefix(dest, "https://") &&
		!strings.HasPrefix(dest, "data:") {
		src = r.asset(dest)
	}
	if src == "" {
		w.WriteString(`<span class="missing">нет вложения: ` + html.EscapeString(dest) + `</span>`)
		return ast.WalkSkipChildren, nil
	}

	alt := string(n.Text(source))
	w.WriteString(`<img class="embed" src="` + html.EscapeString(src) +
		`" alt="` + html.EscapeString(alt) + `" loading="lazy">`)
	return ast.WalkSkipChildren, nil
}

func (r *svodRenderer) asset(target string) string {
	if r.opts.Asset == nil {
		return ""
	}
	if u, err := url.QueryUnescape(target); err == nil {
		target = u
	}
	return r.opts.Asset(target)
}

// ───────────────────────── сборка ─────────────────────────

// HTML собирает готовую разметку заметки.
func HTML(source []byte, opts Options) string {
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
			parser.WithInlineParsers(
				util.Prioritized(wikiParser{}, 100),
				util.Prioritized(mathParser{}, 101),
				util.Prioritized(tagParser{}, 102),
			),
		),
		// Сырой HTML из заметки не выводим: содержимое свода попадает
		// на страницу, доступную без ключа. В goldmark это поведение
		// по умолчанию — включать WithUnsafe нельзя.
		goldmark.WithRendererOptions(
			renderer.WithNodeRenderers(util.Prioritized(&svodRenderer{opts: opts}, 1)),
		),
	)

	var buf bytes.Buffer
	if err := md.Convert(stripFrontmatterAndTitle(source), &buf); err != nil {
		return `<p class="missing">не смог разобрать заметку</p>`
	}
	return buf.String()
}

var (
	reFrontmatter = regexp.MustCompile(`(?s)\A\x{FEFF}?---\r?\n.*?\r?\n---[ \t]*\r?\n?`)
	reFirstH1     = regexp.MustCompile(`\A\s*#\s+[^\n]*\n?`)
)

// stripFrontmatterAndTitle убирает то, что страница показывает отдельно
// в шапке: служебную шапку и первый заголовок.
func stripFrontmatterAndTitle(src []byte) []byte {
	out := reFrontmatter.ReplaceAll(src, nil)
	return reFirstH1.ReplaceAll(out, nil)
}

// HasMath сообщает, нужен ли странице KaTeX.
func HasMath(h string) bool { return strings.Contains(h, `class="math-src`) }

// Title достаёт заголовок для шапки: сперва frontmatter, потом первый H1,
// в последнюю очередь имя файла.
func Title(src []byte, fallbackPath string) string {
	if m := reFirstH1.Find(reFrontmatter.ReplaceAll(src, nil)); m != nil {
		return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(m)), "#"))
	}
	return strings.TrimSuffix(path.Base(fallbackPath), ".md")
}
