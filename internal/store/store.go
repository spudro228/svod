// Package store — серверное хранилище: содержимое в блобах по sha256,
// метаданные и лог изменений в SQLite.
//
// Содержимое адресуется по хешу, поэтому история версий получается
// побочным эффектом: старый блоб просто никто не удаляет.
package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/spudro228/svod/internal/index"
	"github.com/spudro228/svod/internal/proto"
)

// ErrConflict — на сервере лежит не та версия, от которой отталкивался клиент.
var ErrConflict = errors.New("conflict")

// ErrNotFound — такого пути в своде нет.
var ErrNotFound = errors.New("not found")

type Store struct {
	db      *sql.DB
	blobDir string
	fts     bool // удалось ли поднять FTS5
}

const schema = `
CREATE TABLE IF NOT EXISTS files (
  path    TEXT PRIMARY KEY,
  hash    TEXT NOT NULL,
  size    INTEGER NOT NULL,
  mtime   INTEGER NOT NULL,
  seq     INTEGER NOT NULL,
  deleted INTEGER NOT NULL DEFAULT 0,
  title   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS files_seq ON files(seq);

CREATE TABLE IF NOT EXISTS versions (
  seq     INTEGER PRIMARY KEY,
  path    TEXT NOT NULL,
  hash    TEXT NOT NULL,
  deleted INTEGER NOT NULL DEFAULT 0,
  at      INTEGER NOT NULL,
  device  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS versions_path ON versions(path);

CREATE TABLE IF NOT EXISTS links (src TEXT NOT NULL, dst TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS links_dst ON links(dst);

CREATE TABLE IF NOT EXISTS tags (path TEXT NOT NULL, tag TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS tags_tag ON tags(tag);

CREATE TABLE IF NOT EXISTS meta (k TEXT PRIMARY KEY, v TEXT NOT NULL);
INSERT OR IGNORE INTO meta(k, v) VALUES ('seq', '0');
`

// Open поднимает хранилище в каталоге dir: dir/meta.db и dir/blobs/.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "blobs"), 0o755); err != nil {
		return nil, err
	}
	dsn := filepath.Join(dir, "meta.db") + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// modernc/sqlite не любит параллельных писателей в одном процессе.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("схема: %w", err)
	}

	s := &Store{db: db, blobDir: filepath.Join(dir, "blobs")}

	// FTS5 есть не в каждой сборке SQLite. Если нет — поиск уйдёт на LIKE.
	if _, err := db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS notes_fts
		USING fts5(path UNINDEXED, title, body, tokenize='unicode61 remove_diacritics 2')`); err == nil {
		s.fts = true
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// HasFTS сообщает, доступен ли полнотекстовый индекс.
func (s *Store) HasFTS() bool { return s.fts }

// Seq возвращает текущий номер последнего изменения.
func (s *Store) Seq() (int64, error) {
	var v int64
	err := s.db.QueryRow(`SELECT CAST(v AS INTEGER) FROM meta WHERE k='seq'`).Scan(&v)
	return v, err
}

// Hash возвращает хеш текущей версии пути. Пустая строка — файла нет.
func (s *Store) Hash(path string) (string, error) {
	var h string
	var deleted int
	err := s.db.QueryRow(`SELECT hash, deleted FROM files WHERE path = ?`, path).Scan(&h, &deleted)
	if errors.Is(err, sql.ErrNoRows) || deleted == 1 {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return h, nil
}

// Put сохраняет новую версию файла.
//
// baseHash — хеш версии, от которой отталкивался клиент. Пустая строка
// означает «файла ещё нет». Несовпадение с текущим состоянием даёт ErrConflict:
// ничего не перезаписывается, разбирается клиент.
func (s *Store) Put(path string, content []byte, baseHash, device string) (proto.PutResult, error) {
	hash := hashOf(content)

	cur, err := s.Hash(path)
	if err != nil {
		return proto.PutResult{}, err
	}
	if cur != baseHash {
		// Повторная заливка того же самого — не конфликт, а идемпотентность.
		if cur == hash {
			seq, _ := s.Seq()
			return proto.PutResult{Seq: seq, Hash: hash}, nil
		}
		return proto.PutResult{}, ErrConflict
	}
	if cur == hash {
		seq, _ := s.Seq()
		return proto.PutResult{Seq: seq, Hash: hash}, nil
	}

	if err := s.writeBlob(hash, content); err != nil {
		return proto.PutResult{}, err
	}

	// Вложения не разбираем: индексировать в картинке нечего.
	var parsed index.Parsed
	title := index.TitleFromPath(path)
	if index.IsNote(path) {
		parsed = index.Parse(content)
		if parsed.Title != "" {
			title = parsed.Title
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return proto.PutResult{}, err
	}
	defer tx.Rollback()

	seq, err := nextSeq(tx)
	if err != nil {
		return proto.PutResult{}, err
	}
	now := time.Now().Unix()

	if _, err := tx.Exec(`
		INSERT INTO files(path, hash, size, mtime, seq, deleted, title)
		VALUES(?, ?, ?, ?, ?, 0, ?)
		ON CONFLICT(path) DO UPDATE SET
		  hash=excluded.hash, size=excluded.size, mtime=excluded.mtime,
		  seq=excluded.seq, deleted=0, title=excluded.title`,
		path, hash, len(content), now, seq, title); err != nil {
		return proto.PutResult{}, err
	}
	if _, err := tx.Exec(`INSERT INTO versions(seq, path, hash, deleted, at, device) VALUES(?,?,?,0,?,?)`,
		seq, path, hash, now, device); err != nil {
		return proto.PutResult{}, err
	}

	if err := reindex(tx, s.fts && index.IsNote(path), path, title, parsed); err != nil {
		return proto.PutResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return proto.PutResult{}, err
	}
	return proto.PutResult{Seq: seq, Hash: hash}, nil
}

// Delete кладёт надгробие в лог. Блоб остаётся на диске — история цела.
func (s *Store) Delete(path, baseHash, device string) (proto.PutResult, error) {
	cur, err := s.Hash(path)
	if err != nil {
		return proto.PutResult{}, err
	}
	if cur == "" {
		return proto.PutResult{}, ErrNotFound
	}
	if cur != baseHash {
		return proto.PutResult{}, ErrConflict
	}

	tx, err := s.db.Begin()
	if err != nil {
		return proto.PutResult{}, err
	}
	defer tx.Rollback()

	seq, err := nextSeq(tx)
	if err != nil {
		return proto.PutResult{}, err
	}
	now := time.Now().Unix()

	if _, err := tx.Exec(`UPDATE files SET deleted=1, seq=?, mtime=? WHERE path=?`, seq, now, path); err != nil {
		return proto.PutResult{}, err
	}
	if _, err := tx.Exec(`INSERT INTO versions(seq, path, hash, deleted, at, device) VALUES(?,?,?,1,?,?)`,
		seq, path, cur, now, device); err != nil {
		return proto.PutResult{}, err
	}
	if _, err := tx.Exec(`DELETE FROM links WHERE src=?`, path); err != nil {
		return proto.PutResult{}, err
	}
	if _, err := tx.Exec(`DELETE FROM tags WHERE path=?`, path); err != nil {
		return proto.PutResult{}, err
	}
	if s.fts {
		if _, err := tx.Exec(`DELETE FROM notes_fts WHERE path=?`, path); err != nil {
			return proto.PutResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return proto.PutResult{}, err
	}
	return proto.PutResult{Seq: seq, Hash: ""}, nil
}

// Tree возвращает все живые файлы свода.
func (s *Store) Tree() (proto.Tree, error) {
	seq, err := s.Seq()
	if err != nil {
		return proto.Tree{}, err
	}
	rows, err := s.db.Query(`SELECT path, hash, size, mtime, seq, title
		FROM files WHERE deleted=0 ORDER BY path`)
	if err != nil {
		return proto.Tree{}, err
	}
	defer rows.Close()

	out := proto.Tree{Files: []proto.FileMeta{}, Seq: seq}
	for rows.Next() {
		var f proto.FileMeta
		if err := rows.Scan(&f.Path, &f.Hash, &f.Size, &f.ModTime, &f.Seq, &f.Title); err != nil {
			return proto.Tree{}, err
		}
		out.Files = append(out.Files, f)
	}
	return out, rows.Err()
}

// Changes отдаёт всё, что произошло после seq since.
func (s *Store) Changes(since int64) (proto.Changes, error) {
	seq, err := s.Seq()
	if err != nil {
		return proto.Changes{}, err
	}
	rows, err := s.db.Query(`SELECT path, hash, size, mtime, seq, deleted, title
		FROM files WHERE seq > ? ORDER BY seq`, since)
	if err != nil {
		return proto.Changes{}, err
	}
	defer rows.Close()

	out := proto.Changes{Changes: []proto.FileMeta{}, Seq: seq}
	for rows.Next() {
		var f proto.FileMeta
		var del int
		if err := rows.Scan(&f.Path, &f.Hash, &f.Size, &f.ModTime, &f.Seq, &del, &f.Title); err != nil {
			return proto.Changes{}, err
		}
		f.Deleted = del == 1
		out.Changes = append(out.Changes, f)
	}
	return out, rows.Err()
}

// Note собирает заметку целиком: содержимое плюс всё разобранное.
func (s *Store) Note(path string) (proto.Note, error) {
	var n proto.Note
	var deleted int
	err := s.db.QueryRow(`SELECT path, hash, size, mtime, seq, deleted, title
		FROM files WHERE path=?`, path).
		Scan(&n.Path, &n.Hash, &n.Size, &n.ModTime, &n.Seq, &deleted, &n.Title)
	if errors.Is(err, sql.ErrNoRows) || deleted == 1 {
		return n, ErrNotFound
	}
	if err != nil {
		return n, err
	}

	if !index.IsNote(path) {
		// У вложения нет содержимого для показа — только карточка.
		n.Binary = true
		n.Headings, n.Tags, n.Links, n.Backlinks = nil, nil, nil, nil
		n.Backlinks, err = s.Backlinks(path)
		return n, err
	}

	content, err := s.Blob(n.Hash)
	if err != nil {
		return n, err
	}
	n.Content = string(content)

	parsed := index.Parse(content)
	n.Headings = parsed.Headings
	n.Tags = parsed.Tags
	n.Links = parsed.Links
	n.Aliases = parsed.Aliases
	n.Meta = parsed.Frontmatter

	n.Backlinks, err = s.Backlinks(path)
	return n, err
}

// Backlinks — какие заметки ссылаются на эту.
func (s *Store) Backlinks(path string) ([]string, error) {
	target := index.Normalize(path)
	rows, err := s.db.Query(`
		SELECT DISTINCT l.src FROM links l
		JOIN files f ON f.path = l.src AND f.deleted = 0
		WHERE l.dst = ? OR l.dst = ?
		ORDER BY l.src`, target, path)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Search — полнотекстовый поиск. Без FTS5 откатывается на LIKE.
func (s *Store) Search(q string, limit int) ([]proto.SearchHit, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return []proto.SearchHit{}, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	var rows *sql.Rows
	var err error
	if s.fts {
		// Префиксный поиск: пользователь дописывает слово на ходу.
		rows, err = s.db.Query(`
			SELECT f.path, f.title, snippet(notes_fts, 2, '[', ']', '…', 12)
			FROM notes_fts
			JOIN files f ON f.path = notes_fts.path AND f.deleted = 0
			WHERE notes_fts MATCH ?
			ORDER BY rank LIMIT ?`, ftsQuery(q), limit)
	} else {
		like := "%" + q + "%"
		rows, err = s.db.Query(`
			SELECT path, title, '' FROM files
			WHERE deleted = 0 AND (title LIKE ? OR path LIKE ?)
			ORDER BY path LIMIT ?`, like, like, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []proto.SearchHit{}
	for rows.Next() {
		var h proto.SearchHit
		if err := rows.Scan(&h.Path, &h.Title, &h.Snippet); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// History отдаёт все версии пути, свежие первыми.
func (s *Store) History(path string, limit int) ([]proto.Version, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT seq, hash, deleted, at, device
		FROM versions WHERE path=? ORDER BY seq DESC LIMIT ?`, path, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []proto.Version{}
	for rows.Next() {
		var v proto.Version
		var del int
		if err := rows.Scan(&v.Seq, &v.Hash, &del, &v.At, &v.Device); err != nil {
			return nil, err
		}
		v.Deleted = del == 1
		out = append(out, v)
	}
	return out, rows.Err()
}

// Tags отдаёт все теги свода с числом заметок на каждый.
func (s *Store) Tags() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT t.tag, COUNT(DISTINCT t.path)
		FROM tags t JOIN files f ON f.path = t.path AND f.deleted = 0
		GROUP BY t.tag ORDER BY t.tag`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var tag string
		var n int
		if err := rows.Scan(&tag, &n); err != nil {
			return nil, err
		}
		out[tag] = n
	}
	return out, rows.Err()
}

// ByTag — пути заметок с этим тегом.
func (s *Store) ByTag(tag string) ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT t.path FROM tags t
		JOIN files f ON f.path = t.path AND f.deleted = 0
		WHERE t.tag = ? ORDER BY t.path`, tag)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Blob читает содержимое по хешу.
func (s *Store) Blob(hash string) ([]byte, error) {
	if len(hash) < 4 {
		return nil, ErrNotFound
	}
	b, err := os.ReadFile(s.blobPath(hash))
	if os.IsNotExist(err) {
		return nil, ErrNotFound
	}
	return b, err
}

func (s *Store) blobPath(hash string) string {
	return filepath.Join(s.blobDir, hash[:2], hash[2:])
}

func (s *Store) writeBlob(hash string, content []byte) error {
	p := s.blobPath(hash)
	if _, err := os.Stat(p); err == nil {
		return nil // такой блоб уже есть — содержимое неизменяемо
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func nextSeq(tx *sql.Tx) (int64, error) {
	if _, err := tx.Exec(`UPDATE meta SET v = CAST(CAST(v AS INTEGER) + 1 AS TEXT) WHERE k='seq'`); err != nil {
		return 0, err
	}
	var seq int64
	err := tx.QueryRow(`SELECT CAST(v AS INTEGER) FROM meta WHERE k='seq'`).Scan(&seq)
	return seq, err
}

func reindex(tx *sql.Tx, fts bool, path, title string, p index.Parsed) error {
	if _, err := tx.Exec(`DELETE FROM links WHERE src=?`, path); err != nil {
		return err
	}
	for _, dst := range p.Links {
		if _, err := tx.Exec(`INSERT INTO links(src, dst) VALUES(?, ?)`, path, dst); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM tags WHERE path=?`, path); err != nil {
		return err
	}
	for _, tag := range p.Tags {
		if _, err := tx.Exec(`INSERT INTO tags(path, tag) VALUES(?, ?)`, path, tag); err != nil {
			return err
		}
	}
	if fts {
		if _, err := tx.Exec(`DELETE FROM notes_fts WHERE path=?`, path); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO notes_fts(path, title, body) VALUES(?, ?, ?)`,
			path, title, p.Body); err != nil {
			return err
		}
	}
	return nil
}

// ftsQuery превращает пользовательский ввод в безопасный запрос FTS5:
// каждое слово в кавычках, последнее — с префиксным поиском.
func ftsQuery(q string) string {
	fields := strings.Fields(q)
	parts := make([]string, 0, len(fields))
	for i, f := range fields {
		f = strings.ReplaceAll(f, `"`, "")
		if f == "" {
			continue
		}
		if i == len(fields)-1 {
			parts = append(parts, `"`+f+`"*`)
		} else {
			parts = append(parts, `"`+f+`"`)
		}
	}
	if len(parts) == 0 {
		return `""`
	}
	return strings.Join(parts, " ")
}

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// HashOf — хеш содержимого в том же виде, что использует хранилище.
func HashOf(b []byte) string { return hashOf(b) }
