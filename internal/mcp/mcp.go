// Package mcp — сервер Model Context Protocol поверх Свода.
//
// Позволяет Claude Desktop и другим клиентам MCP искать, читать и писать
// заметки. Работает по stdio: клиент запускает бинарник и общается с ним
// построчным JSON-RPC.
//
// Своей логики здесь нет — только перевод вызовов инструментов в те же
// запросы HTTP API, которыми ходит демон. Поэтому заметка, созданная
// из Claude Desktop, через секунду оказывается на диске в Obsidian:
// её забирает обычная синхронизация.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spudro228/svod/internal/proto"
)

// Version протокола, который понимают текущие клиенты.
const protocolVersion = "2024-11-05"

// ───────────────────────── JSON-RPC ─────────────────────────

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ───────────────────────── сервер ─────────────────────────

type Server struct {
	Server string // адрес Свода
	Token  string
	HC     *http.Client

	// Log пишет диагностику. Обязательно в stderr: stdout занят протоколом,
	// и любая посторонняя строка там ломает разбор на стороне клиента.
	Log io.Writer
}

// Run обслуживает соединение до закрытия входа.
func (s *Server) Run(ctx context.Context, in io.Reader, out io.Writer) error {
	if s.HC == nil {
		s.HC = &http.Client{Timeout: 30 * time.Second}
	}

	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 8<<20)
	enc := json.NewEncoder(out)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			s.logf("не разобрал запрос: %v", err)
			continue
		}

		// Уведомления ответа не требуют.
		if len(req.ID) == 0 {
			continue
		}

		result, rerr := s.dispatch(ctx, req)
		resp := response{JSONRPC: "2.0", ID: req.ID}
		if rerr != nil {
			resp.Error = rerr
		} else {
			resp.Result = result
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func (s *Server) dispatch(ctx context.Context, req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "svod", "version": "1.0"},
		}, nil

	case "ping":
		return map[string]any{}, nil

	case "tools/list":
		return map[string]any{"tools": tools()}, nil

	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{Code: -32602, Message: "не разобрал параметры вызова"}
		}
		text, err := s.call(ctx, p.Name, p.Arguments)
		if err != nil {
			// Ошибку возвращаем содержимым, а не сбоем протокола:
			// клиенту полезнее прочитать причину, чем увидеть разрыв.
			return toolResult(err.Error(), true), nil
		}
		return toolResult(text, false), nil

	default:
		return nil, &rpcError{Code: -32601, Message: "неизвестный метод: " + req.Method}
	}
}

func toolResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
}

func (s *Server) logf(format string, args ...any) {
	if s.Log != nil {
		fmt.Fprintf(s.Log, format+"\n", args...)
	}
}

// ───────────────────────── инструменты ─────────────────────────

func tools() []map[string]any {
	str := func(desc string) map[string]any {
		return map[string]any{"type": "string", "description": desc}
	}
	schema := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{
			"type":       "object",
			"properties": props,
			"required":   required,
		}
	}

	return []map[string]any{
		{
			"name":        "search_notes",
			"description": "Полнотекстовый поиск по заметкам свода. Возвращает пути и фрагменты с совпадениями.",
			"inputSchema": schema(map[string]any{
				"query": str("что искать"),
				"limit": map[string]any{"type": "integer", "description": "сколько результатов, по умолчанию 20"},
			}, "query"),
		},
		{
			"name":        "read_note",
			"description": "Прочитать заметку целиком: текст, теги и заметки, которые на неё ссылаются.",
			"inputSchema": schema(map[string]any{
				"path": str("путь заметки, например Ачивки/2026_2/Заметка.md"),
			}, "path"),
		},
		{
			"name":        "list_notes",
			"description": "Список заметок свода. Без фильтра возвращает все пути, с фильтром — совпадающие по подстроке.",
			"inputSchema": schema(map[string]any{
				"filter": str("подстрока пути, необязательно"),
				"limit":  map[string]any{"type": "integer", "description": "сколько путей вернуть, по умолчанию 100"},
			}),
		},
		{
			"name":        "list_recent",
			"description": "Что менялось в своде последним.",
			"inputSchema": schema(map[string]any{
				"limit": map[string]any{"type": "integer", "description": "сколько записей, по умолчанию 20"},
			}),
		},
		{
			"name":        "create_note",
			"description": "Создать заметку. Если файл уже существует, вернётся ошибка — для дописывания есть append_note.",
			"inputSchema": schema(map[string]any{
				"path":    str("путь новой заметки, оканчивается на .md"),
				"content": str("содержимое в markdown"),
			}, "path", "content"),
		},
		{
			"name":        "append_note",
			"description": "Дописать текст в конец существующей заметки.",
			"inputSchema": schema(map[string]any{
				"path": str("путь заметки"),
				"text": str("что дописать"),
			}, "path", "text"),
		},
	}
}

func (s *Server) call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	switch name {
	case "search_notes":
		var a struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		json.Unmarshal(args, &a)
		return s.search(ctx, a.Query, a.Limit)

	case "read_note":
		var a struct {
			Path string `json:"path"`
		}
		json.Unmarshal(args, &a)
		return s.read(ctx, a.Path)

	case "list_notes":
		var a struct {
			Filter string `json:"filter"`
			Limit  int    `json:"limit"`
		}
		json.Unmarshal(args, &a)
		return s.list(ctx, a.Filter, a.Limit)

	case "list_recent":
		var a struct {
			Limit int `json:"limit"`
		}
		json.Unmarshal(args, &a)
		return s.recent(ctx, a.Limit)

	case "create_note":
		var a struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		json.Unmarshal(args, &a)
		return s.create(ctx, a.Path, a.Content)

	case "append_note":
		var a struct {
			Path string `json:"path"`
			Text string `json:"text"`
		}
		json.Unmarshal(args, &a)
		return s.append(ctx, a.Path, a.Text)

	default:
		return "", fmt.Errorf("нет такого инструмента: %s", name)
	}
}

// ───────────────────────── действия ─────────────────────────

func (s *Server) search(ctx context.Context, query string, limit int) (string, error) {
	if strings.TrimSpace(query) == "" {
		return "", errors.New("пустой запрос")
	}
	if limit <= 0 {
		limit = 20
	}

	var out struct {
		Hits []proto.SearchHit `json:"hits"`
	}
	if err := s.get(ctx, fmt.Sprintf("/api/v1/search?q=%s&limit=%d",
		url.QueryEscape(query), limit), &out); err != nil {
		return "", err
	}
	if len(out.Hits) == 0 {
		return "Ничего не нашлось по запросу: " + query, nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Нашлось %d:\n\n", len(out.Hits))
	for _, h := range out.Hits {
		fmt.Fprintf(&b, "- %s\n  %s\n", h.Path, strings.TrimSpace(h.Snippet))
	}
	return b.String(), nil
}

func (s *Server) read(ctx context.Context, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("не указан путь")
	}
	var n proto.Note
	if err := s.get(ctx, "/api/v1/note/"+escapePath(path), &n); err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\nПуть: %s\n", n.Title, n.Path)
	if len(n.Tags) > 0 {
		fmt.Fprintf(&b, "Теги: %s\n", strings.Join(n.Tags, ", "))
	}
	if len(n.Backlinks) > 0 {
		fmt.Fprintf(&b, "Ссылаются сюда: %s\n", strings.Join(n.Backlinks, ", "))
	}
	fmt.Fprintf(&b, "\n---\n\n%s", n.Content)
	return b.String(), nil
}

func (s *Server) list(ctx context.Context, filter string, limit int) (string, error) {
	if limit <= 0 {
		limit = 100
	}
	var tree proto.Tree
	if err := s.get(ctx, "/api/v1/tree", &tree); err != nil {
		return "", err
	}

	needle := strings.ToLower(strings.TrimSpace(filter))
	var b strings.Builder
	n := 0
	for _, f := range tree.Files {
		if needle != "" && !strings.Contains(strings.ToLower(f.Path), needle) {
			continue
		}
		if n >= limit {
			break
		}
		fmt.Fprintf(&b, "- %s\n", f.Path)
		n++
	}
	if n == 0 {
		return "Ничего не подошло", nil
	}
	return fmt.Sprintf("Заметок: %d\n\n%s", n, b.String()), nil
}

func (s *Server) recent(ctx context.Context, limit int) (string, error) {
	if limit <= 0 {
		limit = 20
	}
	var ch proto.Changes
	if err := s.get(ctx, "/api/v1/changes?since=0", &ch); err != nil {
		return "", err
	}

	// Лог отсортирован по возрастанию: свежее в конце.
	files := ch.Changes
	if len(files) > limit {
		files = files[len(files)-limit:]
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Последние изменения (свод на seq %d):\n\n", ch.Seq)
	for i := len(files) - 1; i >= 0; i-- {
		f := files[i]
		state := "изменена"
		if f.Deleted {
			state = "удалена"
		}
		fmt.Fprintf(&b, "- %s — %s, %s\n", f.Path, state,
			time.Unix(f.ModTime, 0).Format("02.01.2006 15:04"))
	}
	return b.String(), nil
}

func (s *Server) create(ctx context.Context, path, content string) (string, error) {
	if !strings.HasSuffix(strings.ToLower(path), ".md") {
		return "", errors.New("путь должен оканчиваться на .md")
	}

	// Заголовок If-Match не отправляем вовсе: это и означает «файла ещё нет».
	// Если он есть, сервер ответит конфликтом, и мы не затрём чужой текст.
	res, err := s.put(ctx, path, content, "")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Создана заметка %s (seq %d)", path, res.Seq), nil
}

func (s *Server) append(ctx context.Context, path, text string) (string, error) {
	var n proto.Note
	if err := s.get(ctx, "/api/v1/note/"+escapePath(path), &n); err != nil {
		return "", err
	}

	body := n.Content
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	body += "\n" + strings.TrimRight(text, "\n") + "\n"

	// С хешем текущей версии: если заметку успели изменить, сервер
	// откажет, и дописанное не затрёт чужую правку.
	res, err := s.put(ctx, path, body, n.Hash)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Дописано в %s (seq %d)", path, res.Seq), nil
}

// ───────────────────────── HTTP ─────────────────────────

func (s *Server) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.Server+path, nil)
	if err != nil {
		return err
	}
	s.authorize(req)

	res, err := s.HC.Do(req)
	if err != nil {
		return fmt.Errorf("свод недоступен: %w", err)
	}
	defer res.Body.Close()

	if err := checkStatus(res); err != nil {
		return err
	}
	return json.NewDecoder(res.Body).Decode(out)
}

func (s *Server) put(ctx context.Context, path, content, baseHash string) (proto.PutResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		s.Server+"/api/v1/files/"+escapePath(path), strings.NewReader(content))
	if err != nil {
		return proto.PutResult{}, err
	}
	s.authorize(req)
	req.Header.Set("Content-Type", "text/markdown; charset=utf-8")
	if baseHash != "" {
		req.Header.Set(proto.HeaderIfMatch, baseHash)
	}

	res, err := s.HC.Do(req)
	if err != nil {
		return proto.PutResult{}, fmt.Errorf("свод недоступен: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusConflict {
		return proto.PutResult{}, errors.New(
			"заметку изменили с другой стороны — перечитай её и попробуй снова")
	}
	if err := checkStatus(res); err != nil {
		return proto.PutResult{}, err
	}

	var out proto.PutResult
	err = json.NewDecoder(res.Body).Decode(&out)
	return out, err
}

func (s *Server) authorize(req *http.Request) {
	if s.Token != "" {
		req.Header.Set("Authorization", "Bearer "+s.Token)
	}
	// Имя устройства едет в процентной кодировке: заголовки только Latin-1.
	req.Header.Set(proto.HeaderDevice, proto.EncodeDevice("MCP"))
}

func checkStatus(res *http.Response) error {
	switch res.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized:
		return errors.New("свод не принял токен: проверь SVOD_TOKEN")
	case http.StatusNotFound:
		return errors.New("такой заметки нет")
	default:
		b, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("свод ответил %s: %s", res.Status, strings.TrimSpace(string(b)))
	}
}

// escapePath кодирует каждый сегмент отдельно: разделители остаются
// разделителями, кириллица и пробелы уезжают в проценты.
func escapePath(rel string) string {
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}
