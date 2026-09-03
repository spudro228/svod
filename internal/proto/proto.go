// Package proto описывает типы и правила протокола синхронизации.
// Пакет общий для сервера и демона — контракт описан здесь один раз.
package proto

import "net/url"

// HeaderIfMatch несёт хеш версии, от которой клиент отталкивался.
// Заголовок отсутствует — клиент считает, что файла на сервере ещё нет.
const HeaderIfMatch = "If-Match"

// HeaderDevice — имя устройства, попадает в историю версий.
//
// Значение передаётся в процентном кодировании: заголовки HTTP обязаны
// быть Latin-1, а имена машин у нас бывают кириллицей. Браузерный fetch
// на нарушении этого правила падает с TypeError ещё до отправки запроса.
const HeaderDevice = "X-Svod-Device"

// EncodeDevice готовит имя устройства к передаче в заголовке.
func EncodeDevice(name string) string { return url.QueryEscape(name) }

// DecodeDevice разбирает имя обратно. Незакодированное значение
// возвращается как есть — старые клиенты продолжают работать.
func DecodeDevice(raw string) string {
	if decoded, err := url.QueryUnescape(raw); err == nil {
		return decoded
	}
	return raw
}

// FileMeta описывает файл на текущий seq либо одну запись в логе изменений.
type FileMeta struct {
	Path    string `json:"path"`
	Hash    string `json:"hash"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"mtime"`
	Seq     int64  `json:"seq"`
	Deleted bool   `json:"deleted,omitempty"`
	Title   string `json:"title,omitempty"`
}

// Changes — ответ на GET /changes?since=N.
type Changes struct {
	Changes []FileMeta `json:"changes"`
	Seq     int64      `json:"seq"`
}

// Tree — плоский список файлов свода. Дерево собирает клиент.
type Tree struct {
	Files []FileMeta `json:"files"`
	Seq   int64      `json:"seq"`
}

// PutResult — успешная заливка.
type PutResult struct {
	Seq  int64  `json:"seq"`
	Hash string `json:"hash"`
}

// Conflict возвращается с кодом 409: на сервере лежит не та версия,
// от которой отталкивался клиент.
type Conflict struct {
	Error      string `json:"error"`
	ServerHash string `json:"server_hash"`
	Seq        int64  `json:"seq"`
}

// Heading — строка оглавления заметки.
type Heading struct {
	Level int    `json:"level"`
	Text  string `json:"text"`
	ID    string `json:"id"`
}

// Note — содержимое плюс всё, что сервер разобрал из markdown.
type Note struct {
	Path      string            `json:"path"`
	Hash      string            `json:"hash"`
	Seq       int64             `json:"seq"`
	Size      int64             `json:"size"`
	ModTime   int64             `json:"mtime"`
	Title     string            `json:"title"`
	Content   string            `json:"content"`
	Headings  []Heading         `json:"headings"`
	Tags      []string          `json:"tags"`
	Links     []string          `json:"links"`
	Backlinks []string          `json:"backlinks"`
	Aliases   []string          `json:"aliases,omitempty"`
	Meta      map[string]string `json:"meta,omitempty"`
	Binary    bool              `json:"binary,omitempty"`
}

// Version — одна запись в истории пути.
type Version struct {
	Seq     int64  `json:"seq"`
	Hash    string `json:"hash"`
	Deleted bool   `json:"deleted,omitempty"`
	At      int64  `json:"at"`
	Device  string `json:"device"`
}

// Share — временная ссылка на просмотр одной заметки без токена.
type Share struct {
	Key     string `json:"key"`
	Path    string `json:"path"`
	Created int64  `json:"created"`
	Expires int64  `json:"expires"`
	URL     string `json:"url,omitempty"`
}

// SearchHit — одно попадание полнотекстового поиска.
type SearchHit struct {
	Path    string `json:"path"`
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
}

// StreamEvent приходит по WebSocket на каждый новый seq.
// Содержимого в нём нет — это только повод сходить за /changes.
type StreamEvent struct {
	Seq  int64  `json:"seq"`
	Path string `json:"path"`
}
