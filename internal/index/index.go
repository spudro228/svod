// Package index разбирает markdown: frontmatter, заголовки, wiki-ссылки, теги.
//
// Разбор идёт по AST goldmark, поэтому содержимое блоков кода не попадает
// ни в ссылки, ни в теги — [[так]] внутри ``` остаётся текстом.
package index

import (
	"bytes"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	meta "github.com/yuin/goldmark-meta"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"

	"github.com/spudro228/svod/internal/proto"
)

var (
	// [[путь/файл]] или [[путь/файл|подпись]]
	reWiki = regexp.MustCompile(`\[\[([^\[\]|]+)(?:\|[^\[\]]*)?\]\]`)
	// ![[вложение.png]] — обсидиановский синтаксис встраивания
	reEmbed = regexp.MustCompile(`!\[\[([^\[\]|]+)(?:\|[^\[\]]*)?\]\]`)
	// #тег — латиница, кириллица, цифры, дефис, подчёркивание, слеш
	reTag = regexp.MustCompile(`(^|\s)#([\p{L}\d][\p{L}\d_/-]*)`)
	// небезопасные для якоря символы
	reSlugDrop = regexp.MustCompile(`[^\p{L}\p{N}\s-]`)
	reSlugGap  = regexp.MustCompile(`[\s-]+`)
)

// Parsed — всё, что мы вытащили из одной заметки.
type Parsed struct {
	Title    string
	Headings []proto.Heading
	Tags     []string
	Links    []string
	// Embeds — вложения, встроенные в текст. Нужны отдельно от Links:
	// по временной ссылке гостю открывают доступ ровно к ним.
	Embeds      []string
	Aliases     []string
	Frontmatter map[string]string
	Body        string // плоский текст для полнотекстового поиска
}

var md = goldmark.New(goldmark.WithExtensions(meta.Meta))

// Parse разбирает содержимое заметки.
func Parse(src []byte) Parsed {
	p := Parsed{
		Headings:    []proto.Heading{},
		Tags:        []string{},
		Links:       []string{},
		Embeds:      []string{},
		Aliases:     []string{},
		Frontmatter: map[string]string{},
	}

	ctx := parser.NewContext()
	doc := md.Parser().Parse(text.NewReader(src), parser.WithContext(ctx))

	// Frontmatter goldmark-meta уже отрезал от документа.
	fm := meta.Get(ctx)
	tagSeen := map[string]bool{}
	for k, v := range fm {
		key := strings.ToLower(strings.TrimSpace(k))
		switch key {
		case "tags", "tag":
			for _, t := range toList(v) {
				t = strings.TrimPrefix(t, "#")
				if t != "" && !tagSeen[t] {
					tagSeen[t] = true
					p.Tags = append(p.Tags, t)
				}
			}
		case "aliases", "alias":
			p.Aliases = append(p.Aliases, toList(v)...)
		case "title":
			p.Title = scalar(v)
		default:
			if s := scalar(v); s != "" {
				p.Frontmatter[k] = s
			}
		}
	}

	var plain bytes.Buffer
	seen := map[string]int{}

	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch node := n.(type) {
		case *ast.Heading:
			txt := nodeText(node, src)
			if txt == "" {
				return ast.WalkContinue, nil
			}
			base := slug(txt)
			id := base
			if seen[base] > 0 {
				id = fmt.Sprintf("%s-%d", base, seen[base])
			}
			seen[base]++
			p.Headings = append(p.Headings, proto.Heading{
				Level: node.Level, Text: txt, ID: id,
			})
			if p.Title == "" && node.Level == 1 {
				p.Title = txt
			}
			plain.WriteString(txt)
			plain.WriteByte('\n')
			return ast.WalkSkipChildren, nil

		case *ast.FencedCodeBlock, *ast.CodeBlock, *ast.CodeSpan:
			// Содержимое кода не индексируем и не сканируем на ссылки.
			return ast.WalkSkipChildren, nil

		case *ast.Image:
			// Обычные картинки markdown: цель лежит в узле, а не в тексте.
			if dst := strings.TrimSpace(string(node.Destination)); dst != "" {
				p.Embeds = append(p.Embeds, Normalize(dst))
			}
			return ast.WalkSkipChildren, nil

		case *ast.Text:
			plain.Write(node.Segment.Value(src))
			if node.SoftLineBreak() || node.HardLineBreak() {
				plain.WriteByte('\n')
			}
		}
		return ast.WalkContinue, nil
	})

	p.Body = plain.String()

	embedSeen := map[string]bool{}
	for _, e := range p.Embeds {
		embedSeen[e] = true
	}
	p.Embeds = p.Embeds[:0]
	for e := range embedSeen {
		p.Embeds = append(p.Embeds, e)
	}
	// ![[…]] остаётся для goldmark простым текстом, поэтому ловим отдельно.
	for _, m := range reEmbed.FindAllStringSubmatch(p.Body, -1) {
		target := strings.TrimSpace(m[1])
		if target != "" && !embedSeen[target] {
			embedSeen[target] = true
			p.Embeds = append(p.Embeds, target)
		}
	}

	linkSeen := map[string]bool{}
	for _, m := range reWiki.FindAllStringSubmatch(p.Body, -1) {
		target := Normalize(strings.TrimSpace(m[1]))
		if target == "" || linkSeen[target] {
			continue
		}
		linkSeen[target] = true
		p.Links = append(p.Links, target)
	}

	for _, m := range reTag.FindAllStringSubmatch(p.Body, -1) {
		if tag := m[2]; !tagSeen[tag] {
			tagSeen[tag] = true
			p.Tags = append(p.Tags, tag)
		}
	}

	return p
}

// Normalize приводит цель wiki-ссылки к виду, в котором хранятся пути:
// без ведущего слеша, без расширения .md, без якоря.
func Normalize(target string) string {
	if i := strings.IndexAny(target, "#"); i >= 0 {
		target = target[:i]
	}
	target = strings.TrimSpace(target)
	target = strings.TrimPrefix(target, "./")
	target = strings.TrimPrefix(target, "/")
	target = strings.TrimSuffix(target, ".md")
	return target
}

// TitleFromPath — запасной заголовок, когда в заметке нет ни H1, ни frontmatter.
func TitleFromPath(p string) string {
	return strings.TrimSuffix(path.Base(p), ".md")
}

// IsNote сообщает, разбираем ли мы этот путь как markdown.
// Вложения хранятся как есть: индексировать в них нечего.
func IsNote(p string) bool {
	return strings.EqualFold(path.Ext(p), ".md")
}

// toList приводит значение frontmatter к списку строк:
// в YAML теги пишут и списком, и строкой через запятую.
func toList(v any) []string {
	switch val := v.(type) {
	case []any:
		out := make([]string, 0, len(val))
		for _, item := range val {
			if s := scalar(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		var out []string
		for _, part := range strings.FieldsFunc(val, func(r rune) bool {
			return r == ',' || r == ' '
		}) {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
		return out
	default:
		if s := scalar(v); s != "" {
			return []string{s}
		}
		return nil
	}
}

func scalar(v any) string {
	switch val := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(val)
	case []any, map[string]any, map[any]any:
		return "" // вложенные структуры в карточку заметки не тащим
	default:
		return strings.TrimSpace(fmt.Sprint(val))
	}
}

func nodeText(n ast.Node, src []byte) string {
	var b bytes.Buffer
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch t := c.(type) {
		case *ast.Text:
			b.Write(t.Segment.Value(src))
		default:
			b.WriteString(nodeText(t, src))
		}
	}
	return strings.TrimSpace(b.String())
}

func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = reSlugDrop.ReplaceAllString(s, "")
	s = reSlugGap.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}
