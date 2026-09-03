package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"time"

	"github.com/spudro228/svod/internal/index"
	"github.com/spudro228/svod/internal/proto"
)

// Хранение временных ссылок «показать заметку без токена».
//
// Это единственное место, где содержимое свода становится доступно
// без ключа, поэтому правила жёсткие:
//
//  1. Ключ случайный, а не производный от пути. Из хеша пути его делать
//     нельзя: разгадав схему, любой подберёт ссылку на соседнюю заметку.
//  2. Одна ссылка открывает ровно один путь. Список вложений фиксируется
//     в момент выдачи — доступа к остальным блобам она не даёт.
//  3. Срок живёт здесь, а не в ссылке и не в куке.
//  4. Истекшая и отозванная неотличимы от несуществующей: иначе по разнице
//     ответов можно было бы выяснять, какие ключи когда-то существовали.

// ErrShareGone — ссылки нет, она отозвана или истекла. Различать эти случаи
// снаружи нельзя намеренно.
var ErrShareGone = errors.New("ссылка недействительна")

const shareSchema = `
CREATE TABLE IF NOT EXISTS shares (
  key     TEXT PRIMARY KEY,
  path    TEXT NOT NULL,
  blobs   TEXT NOT NULL DEFAULT '',
  created INTEGER NOT NULL,
  expires INTEGER NOT NULL,
  revoked INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS shares_path ON shares(path);
`

// CreateShare выдаёт временную ссылку на заметку.
//
// Вместе с путём запоминается список вложений, встроенных в заметку прямо
// сейчас. Без этого картинки у гостя оказались бы битыми, а разрешать
// ему все блобы подряд — значит открыть всё хранилище.
func (s *Store) CreateShare(path string, ttl time.Duration) (proto.Share, error) {
	hash, err := s.Hash(path)
	if err != nil {
		return proto.Share{}, err
	}
	if hash == "" {
		return proto.Share{}, ErrNotFound
	}

	var blobs string
	if index.IsNote(path) {
		content, err := s.Blob(hash)
		if err != nil {
			return proto.Share{}, err
		}
		blobs = s.embeddedBlobs(path, content)
	}

	key, err := newShareKey()
	if err != nil {
		return proto.Share{}, err
	}

	now := time.Now()
	expires := now.Add(ttl)
	if _, err := s.db.Exec(`INSERT INTO shares(key, path, blobs, created, expires, revoked)
		VALUES(?, ?, ?, ?, ?, 0)`,
		key, path, blobs, now.Unix(), expires.Unix()); err != nil {
		return proto.Share{}, err
	}

	return proto.Share{
		Key: key, Path: path,
		Created: now.Unix(), Expires: expires.Unix(),
	}, nil
}

// Share возвращает живую ссылку. Истекшая, отозванная и несуществующая
// дают одну и ту же ошибку.
func (s *Store) Share(key string) (proto.Share, []string, error) {
	var sh proto.Share
	var blobs string
	var revoked int

	err := s.db.QueryRow(`SELECT key, path, blobs, created, expires, revoked
		FROM shares WHERE key = ?`, key).
		Scan(&sh.Key, &sh.Path, &blobs, &sh.Created, &sh.Expires, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return proto.Share{}, nil, ErrShareGone
	}
	if err != nil {
		return proto.Share{}, nil, err
	}
	if revoked == 1 || time.Now().Unix() > sh.Expires {
		return proto.Share{}, nil, ErrShareGone
	}

	var list []string
	if blobs != "" {
		list = splitList(blobs)
	}
	return sh, list, nil
}

// RevokeShare гасит ссылку немедленно.
func (s *Store) RevokeShare(key string) error {
	res, err := s.db.Exec(`UPDATE shares SET revoked = 1 WHERE key = ?`, key)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrShareGone
	}
	return nil
}

// Shares перечисляет живые ссылки — чтобы владелец видел, что открыто.
func (s *Store) Shares() ([]proto.Share, error) {
	rows, err := s.db.Query(`SELECT key, path, created, expires FROM shares
		WHERE revoked = 0 AND expires > ? ORDER BY created DESC`, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []proto.Share{}
	for rows.Next() {
		var sh proto.Share
		if err := rows.Scan(&sh.Key, &sh.Path, &sh.Created, &sh.Expires); err != nil {
			return nil, err
		}
		out = append(out, sh)
	}
	return out, rows.Err()
}

// embeddedBlobs собирает хеши вложений, встроенных в заметку.
//
// Разрешаем ровно их и ничего больше: именно здесь проще всего
// случайно открыть гостю доступ ко всему хранилищу блобов.
func (s *Store) embeddedBlobs(notePath string, content []byte) string {
	parsed := index.Parse(content)
	seen := map[string]bool{}
	var hashes []string

	for _, link := range parsed.Embeds {
		resolved := s.resolveAttachment(notePath, link)
		if resolved == "" {
			continue
		}
		h, err := s.Hash(resolved)
		if err != nil || h == "" || seen[h] {
			continue
		}
		seen[h] = true
		hashes = append(hashes, h)
	}
	return joinList(hashes)
}

// resolveAttachment ищет вложение так же, как это делает страница:
// сперва по полному пути, затем по имени файла.
func (s *Store) resolveAttachment(notePath, target string) string {
	if h, _ := s.Hash(target); h != "" {
		return target
	}
	var found string
	err := s.db.QueryRow(`SELECT path FROM files
		WHERE deleted = 0 AND (path = ? OR path LIKE ?) ORDER BY LENGTH(path) LIMIT 1`,
		target, "%/"+baseName(target)).Scan(&found)
	if err != nil {
		return ""
	}
	return found
}

func newShareKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func baseName(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

func joinList(items []string) string {
	out := ""
	for i, it := range items {
		if i > 0 {
			out += "\n"
		}
		out += it
	}
	return out
}

func splitList(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '\n' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}
