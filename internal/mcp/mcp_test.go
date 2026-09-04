package mcp_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/spudro228/svod/internal/api"
	"github.com/spudro228/svod/internal/mcp"
	"github.com/spudro228/svod/internal/store"
)

const token = "mcp-test-token"

// Поднимаем настоящий Свод: MCP-сервер ходит в него по HTTP,
// как и в жизни, поэтому подменять нечего.
func newSvod(t *testing.T) (string, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	fsys := fstest.MapFS{"index.html": {Data: []byte("<html></html>")}}
	srv := httptest.NewServer(api.New(st, token, nil, fsys,
		slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(srv.Close)
	return srv.URL, st
}

// call прогоняет один вызов инструмента через настоящий протокол:
// строка JSON-RPC на входе, строка на выходе.
func call(t *testing.T, svodURL, name string, args map[string]any) (string, bool) {
	t.Helper()

	argsJSON, _ := json.Marshal(args)
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":` +
			`{"name":"` + name + `","arguments":` + string(argsJSON) + `}}` + "\n")

	var out strings.Builder
	s := &mcp.Server{Server: svodURL, Token: token, HC: &http.Client{}, Log: io.Discard}
	if err := s.Run(context.Background(), in, &out); err != nil {
		t.Fatalf("сервер оборвался: %v", err)
	}

	var resp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &resp); err != nil {
		t.Fatalf("не разобрал ответ %q: %v", out.String(), err)
	}
	if resp.Error != nil {
		t.Fatalf("протокольная ошибка: %s", resp.Error.Message)
	}
	if len(resp.Result.Content) == 0 {
		t.Fatal("пустой ответ инструмента")
	}
	return resp.Result.Content[0].Text, resp.Result.IsError
}

func TestРукопожатиеИСписокИнструментов(t *testing.T) {
	url, _ := newSvod(t)

	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
			`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n")

	var out strings.Builder
	s := &mcp.Server{Server: url, Token: token, HC: &http.Client{}, Log: io.Discard}
	if err := s.Run(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	// На уведомление отвечать нельзя — иначе клиент решит, что сервер
	// не понимает протокол.
	if len(lines) != 2 {
		t.Fatalf("ожидал два ответа на два запроса, получил %d: %q", len(lines), out.String())
	}

	var init struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	json.Unmarshal([]byte(lines[0]), &init)
	if init.Result.ProtocolVersion == "" || init.Result.ServerInfo.Name != "svod" {
		t.Errorf("странное рукопожатие: %+v", init.Result)
	}

	var list struct {
		Result struct {
			Tools []struct {
				Name        string         `json:"name"`
				InputSchema map[string]any `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	json.Unmarshal([]byte(lines[1]), &list)

	want := []string{"search_notes", "read_note", "list_notes", "list_recent", "create_note", "append_note"}
	got := map[string]bool{}
	for _, tool := range list.Result.Tools {
		got[tool.Name] = true
		if tool.InputSchema == nil {
			t.Errorf("у инструмента %s нет описания параметров", tool.Name)
		}
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("нет инструмента %s", w)
		}
	}
}

func TestСозданиеИЧтениеЗаметки(t *testing.T) {
	url, _ := newSvod(t)

	text, isErr := call(t, url, "create_note", map[string]any{
		"path":    "Из Клода/Первая.md",
		"content": "# Первая из Клода\n\nтекст, созданный через MCP\n",
	})
	if isErr {
		t.Fatalf("создание не удалось: %s", text)
	}

	read, isErr := call(t, url, "read_note", map[string]any{"path": "Из Клода/Первая.md"})
	if isErr {
		t.Fatalf("чтение не удалось: %s", read)
	}
	if !strings.Contains(read, "текст, созданный через MCP") {
		t.Errorf("прочитано не то: %q", read)
	}
	if !strings.Contains(read, "Первая из Клода") {
		t.Error("в ответе нет заголовка")
	}
}

// Создание не должно затирать существующую заметку.
func TestСозданиеНеЗатираетСуществующую(t *testing.T) {
	url, _ := newSvod(t)

	if _, isErr := call(t, url, "create_note", map[string]any{
		"path": "Занято.md", "content": "# Занято\n\nважный текст\n",
	}); isErr {
		t.Fatal("первое создание не удалось")
	}

	text, isErr := call(t, url, "create_note", map[string]any{
		"path": "Занято.md", "content": "# Занято\n\nчужой текст\n",
	})
	if !isErr {
		t.Fatal("повторное создание прошло и затёрло заметку")
	}
	if !strings.Contains(text, "изменили") {
		t.Errorf("непонятное сообщение об отказе: %q", text)
	}

	read, _ := call(t, url, "read_note", map[string]any{"path": "Занято.md"})
	if !strings.Contains(read, "важный текст") {
		t.Error("исходный текст всё-таки потерян")
	}
}

func TestДописываниеСохраняетСтарое(t *testing.T) {
	url, _ := newSvod(t)

	call(t, url, "create_note", map[string]any{
		"path": "Дневник.md", "content": "# Дневник\n\nпервая запись\n",
	})
	text, isErr := call(t, url, "append_note", map[string]any{
		"path": "Дневник.md", "text": "вторая запись",
	})
	if isErr {
		t.Fatalf("дописать не удалось: %s", text)
	}

	read, _ := call(t, url, "read_note", map[string]any{"path": "Дневник.md"})
	if !strings.Contains(read, "первая запись") || !strings.Contains(read, "вторая запись") {
		t.Errorf("после дописывания текст неполон: %q", read)
	}
}

func TestПоискНаходитПоСодержимому(t *testing.T) {
	url, _ := newSvod(t)

	call(t, url, "create_note", map[string]any{
		"path": "Кафка.md", "content": "# Кафка\n\nидемпотентность продюсера\n",
	})
	call(t, url, "create_note", map[string]any{
		"path": "Постгрес.md", "content": "# Постгрес\n\nиндексы и планы\n",
	})

	text, isErr := call(t, url, "search_notes", map[string]any{"query": "идемпотентность"})
	if isErr {
		t.Fatalf("поиск не удался: %s", text)
	}
	if !strings.Contains(text, "Кафка.md") {
		t.Errorf("нужная заметка не найдена: %q", text)
	}
	if strings.Contains(text, "Постгрес.md") {
		t.Errorf("в выдачу попала посторонняя заметка: %q", text)
	}
}

func TestСписокИПоследниеИзменения(t *testing.T) {
	url, _ := newSvod(t)

	call(t, url, "create_note", map[string]any{"path": "Папка/Раз.md", "content": "# Раз\n"})
	call(t, url, "create_note", map[string]any{"path": "Папка/Два.md", "content": "# Два\n"})
	call(t, url, "create_note", map[string]any{"path": "Другое/Три.md", "content": "# Три\n"})

	all, _ := call(t, url, "list_notes", map[string]any{})
	for _, want := range []string{"Папка/Раз.md", "Папка/Два.md", "Другое/Три.md"} {
		if !strings.Contains(all, want) {
			t.Errorf("в списке нет %s", want)
		}
	}

	filtered, _ := call(t, url, "list_notes", map[string]any{"filter": "Папка"})
	if strings.Contains(filtered, "Другое/Три.md") {
		t.Error("фильтр не сработал")
	}

	recent, _ := call(t, url, "list_recent", map[string]any{"limit": 2})
	if !strings.Contains(recent, "Другое/Три.md") {
		t.Errorf("свежее изменение не в списке: %q", recent)
	}
}

// Без токена MCP не должен ничего доставать — и должен сказать об этом
// понятно, а не молча вернуть пустоту.
func TestБезТокенаПонятнаяОшибка(t *testing.T) {
	url, _ := newSvod(t)

	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":` +
			`{"name":"list_notes","arguments":{}}}` + "\n")
	var out strings.Builder
	s := &mcp.Server{Server: url, Token: "", HC: &http.Client{}, Log: io.Discard}
	s.Run(context.Background(), in, &out)

	if !strings.Contains(out.String(), "SVOD_TOKEN") {
		t.Errorf("непонятное сообщение об отказе: %q", out.String())
	}
}

func TestНеизвестныйИнструмент(t *testing.T) {
	url, _ := newSvod(t)
	text, isErr := call(t, url, "удали_всё", map[string]any{})
	if !isErr {
		t.Fatal("несуществующий инструмент выполнился")
	}
	if !strings.Contains(text, "нет такого инструмента") {
		t.Errorf("непонятная ошибка: %q", text)
	}
}

// Пути с кириллицей и эмодзи должны ходить целыми.
func TestНепростыеПути(t *testing.T) {
	url, _ := newSvod(t)
	path := "📖 Go/Ачивки 2026/Микросервис — СДЭК.md"

	if _, isErr := call(t, url, "create_note", map[string]any{
		"path": path, "content": "# СДЭК\n\nинтеграция\n",
	}); isErr {
		t.Fatal("не создалась заметка со сложным путём")
	}
	read, isErr := call(t, url, "read_note", map[string]any{"path": path})
	if isErr || !strings.Contains(read, "интеграция") {
		t.Errorf("не прочиталась: %q", read)
	}
}
