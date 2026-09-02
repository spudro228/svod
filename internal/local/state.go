// Package local хранит состояние демона: карту «путь → хеш» на момент
// последней синхронизации и номер, до которого демон дочитал лог сервера.
package local

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type State struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS files (
  path TEXT PRIMARY KEY,
  hash TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS meta (k TEXT PRIMARY KEY, v TEXT NOT NULL);
INSERT OR IGNORE INTO meta(k, v) VALUES ('last_seq', '0');
`

// Open создаёт .svod/state.db внутри свода.
func Open(vault string) (*State, error) {
	dir := filepath.Join(vault, ".svod")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	dsn := filepath.Join(dir, "state.db") + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	return &State{db: db}, nil
}

func (s *State) Close() error { return s.db.Close() }

// Hash возвращает известный демону хеш пути. Пустая строка — путь новый.
func (s *State) Hash(path string) (string, error) {
	var h string
	err := s.db.QueryRow(`SELECT hash FROM files WHERE path=?`, path).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return h, err
}

// Set запоминает хеш, с которым путь синхронизирован.
func (s *State) Set(path, hash string) error {
	_, err := s.db.Exec(`INSERT INTO files(path, hash) VALUES(?, ?)
		ON CONFLICT(path) DO UPDATE SET hash=excluded.hash`, path, hash)
	return err
}

// Forget убирает путь из состояния.
func (s *State) Forget(path string) error {
	_, err := s.db.Exec(`DELETE FROM files WHERE path=?`, path)
	return err
}

// All отдаёт всю карту «путь → хеш».
func (s *State) All() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT path, hash FROM files`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var p, h string
		if err := rows.Scan(&p, &h); err != nil {
			return nil, err
		}
		out[p] = h
	}
	return out, rows.Err()
}

// LastSeq — до какого номера демон дочитал лог сервера.
func (s *State) LastSeq() (int64, error) {
	var v int64
	err := s.db.QueryRow(`SELECT CAST(v AS INTEGER) FROM meta WHERE k='last_seq'`).Scan(&v)
	return v, err
}

func (s *State) SetLastSeq(seq int64) error {
	_, err := s.db.Exec(`UPDATE meta SET v=CAST(? AS TEXT) WHERE k='last_seq'`, seq)
	return err
}
