package index_test

import (
	"slices"
	"testing"

	"github.com/spudro228/svod/internal/index"
)

func TestFrontmatter(t *testing.T) {
	src := []byte(`---
title: Настоящий заголовок
tags: [go, vk, доставка]
aliases:
  - СДЭК
  - cdek
status: в работе
---

# Заголовок из текста

Текст с тегом #ещёодин и ссылкой [[Другая заметка]].
`)

	p := index.Parse(src)

	// Заголовок из frontmatter важнее, чем H1 в тексте.
	if p.Title != "Настоящий заголовок" {
		t.Errorf("title: получил %q", p.Title)
	}
	for _, want := range []string{"go", "vk", "доставка", "ещёодин"} {
		if !slices.Contains(p.Tags, want) {
			t.Errorf("тег %q потерян, есть: %v", want, p.Tags)
		}
	}
	if !slices.Contains(p.Aliases, "СДЭК") {
		t.Errorf("алиасы: %v", p.Aliases)
	}
	if p.Frontmatter["status"] != "в работе" {
		t.Errorf("произвольное поле потеряно: %v", p.Frontmatter)
	}
	if !slices.Contains(p.Links, "Другая заметка") {
		t.Errorf("ссылки: %v", p.Links)
	}
}

// Теги и ссылки внутри блока кода — это текст, а не разметка.
func TestКодНеИндексируется(t *testing.T) {
	src := []byte("# Заметка\n\nОбычный #тег.\n\n```go\n// [[НеСсылка]] и #нетег\nfmt.Println(\"#тоженетег\")\n```\n\nИ `#инлайннетег` тоже.\n")

	p := index.Parse(src)

	if slices.Contains(p.Links, "НеСсылка") {
		t.Errorf("ссылка из блока кода попала в индекс: %v", p.Links)
	}
	for _, bad := range []string{"нетег", "тоженетег", "инлайннетег"} {
		if slices.Contains(p.Tags, bad) {
			t.Errorf("тег %q из кода попал в индекс: %v", bad, p.Tags)
		}
	}
	if !slices.Contains(p.Tags, "тег") {
		t.Errorf("настоящий тег потерян: %v", p.Tags)
	}
}

func TestЗаголовкиИЯкоря(t *testing.T) {
	src := []byte("# Один\n\n## Раздел\n\ntext\n\n## Раздел\n\ntext\n")

	p := index.Parse(src)
	if len(p.Headings) != 3 {
		t.Fatalf("ожидал три заголовка, получил %d: %+v", len(p.Headings), p.Headings)
	}
	// Одинаковые заголовки должны получить разные якоря.
	if p.Headings[1].ID == p.Headings[2].ID {
		t.Errorf("якоря совпали: %q", p.Headings[1].ID)
	}
}

func TestВложенияНеЗаметки(t *testing.T) {
	if index.IsNote("img/Снимок.png") {
		t.Error("картинку считаем заметкой")
	}
	if !index.IsNote("Ачивки/2026_2/СДЭК.md") {
		t.Error("заметку не считаем заметкой")
	}
}
