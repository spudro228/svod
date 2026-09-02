// Package index разбирает markdown: заголовки, wiki-ссылки, теги.
// Разбор идёт по AST goldmark, поэтому содержимое блоков кода не попадает
// ни в ссылки, ни в теги — [[так]] внутри ``` остаётся текстом.
package index

import (
	"bytes"
	"path"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"

	"github.com/spudro228/svod/internal/proto"
)

var (
	// [[путь/файл]] или [[путь/файл|подпись]]
	reWiki = regexp.MustCompile(`\[\[([^\[\]|]+)(?:\|[^\[\]]*)?\]\]`)
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
	Body     string // плоский текст для полнотекстового поиска
}

var md = goldmark.New()

// Parse разбирает содержимое заметки.
func Parse(src []byte) Parsed {
	p := Parsed{
		Headings: []proto.Heading{},
		Tags:     []string{},
		Links:    []string{},
	}

	reader := text.NewReader(src)
	doc := md.Parser().Parse(reader)

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
			id := slug(txt)
			if seen[id] > 0 {
				id = id + "-" + itoa(seen[id])
			}
			seen[slug(txt)]++
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

		case *ast.Text:
			plain.Write(node.Segment.Value(src))
			if node.SoftLineBreak() || node.HardLineBreak() {
				plain.WriteByte('\n')
			}
		}
		return ast.WalkContinue, nil
	})

	p.Body = plain.String()

	linkSeen := map[string]bool{}
	for _, m := range reWiki.FindAllStringSubmatch(p.Body, -1) {
		target := Normalize(strings.TrimSpace(m[1]))
		if target == "" || linkSeen[target] {
			continue
		}
		linkSeen[target] = true
		p.Links = append(p.Links, target)
	}

	tagSeen := map[string]bool{}
	for _, m := range reTag.FindAllStringSubmatch(p.Body, -1) {
		tag := m[2]
		if tagSeen[tag] {
			continue
		}
		tagSeen[tag] = true
		p.Tags = append(p.Tags, tag)
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

// TitleFromPath — запасной заголовок, когда в заметке нет H1.
func TitleFromPath(p string) string {
	base := path.Base(p)
	return strings.TrimSuffix(base, ".md")
}

func nodeText(n ast.Node, src []byte) string {
	var b bytes.Buffer
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch t := c.(type) {
		case *ast.Text:
			b.Write(t.Segment.Value(src))
		case *ast.CodeSpan:
			b.WriteString(nodeText(t, src))
		default:
			b.WriteString(nodeText(c, src))
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

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
