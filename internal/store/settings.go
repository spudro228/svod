package store

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// Настройки показа: то, что относится к виду свода, а не к его содержимому.
//
// Живут на сервере, а не в браузере, чтобы порядок папок переезжал
// на телефон и на вторую машину вместе со всем остальным.

// Setting читает значение. Отсутствующий ключ — не ошибка.
func (s *Store) Setting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT v FROM meta WHERE k = ?`, "setting:"+key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetSetting сохраняет значение.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO meta(k, v) VALUES(?, ?)
		ON CONFLICT(k) DO UPDATE SET v = excluded.v`, "setting:"+key, value)
	return err
}

// RootOrder — заданный пользователем порядок корневых папок.
// Папки, которых в списке нет, показываются после него по алфавиту.
func (s *Store) RootOrder() ([]string, error) {
	raw, err := s.Setting("root_order")
	if err != nil || raw == "" {
		return []string{}, err
	}
	var order []string
	if err := json.Unmarshal([]byte(raw), &order); err != nil {
		// Испорченное значение не должно ломать показ дерева:
		// просто вернёмся к алфавиту.
		return []string{}, nil
	}
	return order, nil
}

// SetRootOrder сохраняет порядок. Список чистится от пустых имён
// и повторов: он приходит из браузера, и полагаться на него нельзя.
func (s *Store) SetRootOrder(order []string) error {
	seen := map[string]bool{}
	clean := make([]string, 0, len(order))
	for _, name := range order {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		clean = append(clean, name)
	}
	if len(clean) > 500 {
		clean = clean[:500]
	}

	raw, err := json.Marshal(clean)
	if err != nil {
		return err
	}
	return s.SetSetting("root_order", string(raw))
}
