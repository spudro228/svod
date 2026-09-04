package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/spudro228/svod/internal/store"
)

func open(t *testing.T) *store.Store {
	t.Helper()
	return openAt(t, t.TempDir())
}

// openAt нужен там, где важно переоткрыть то же хранилище.
func openAt(t *testing.T, dir string) *store.Store {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("хранилище: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestСрокСсылкиИстекает(t *testing.T) {
	st := open(t)
	if _, err := st.Put("Заметка.md", []byte("# Заметка\n"), "", "тест"); err != nil {
		t.Fatal(err)
	}

	// Отрицательный срок означает, что ссылка истекла в момент выдачи.
	sh, err := st.CreateShare("Заметка.md", -time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Share(sh.Key); !errors.Is(err, store.ErrShareGone) {
		t.Fatalf("истекшая ссылка всё ещё открывается: %v", err)
	}

	// И в списке живых её быть не должно.
	list, err := st.Shares()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("истекшая ссылка попала в список живых: %+v", list)
	}
}

// Вместе со ссылкой фиксируются вложения именно этой заметки.
// Здесь легче всего было бы открыть гостю всё хранилище блобов.
func TestСсылкаРазрешаетТолькоСвоиВложения(t *testing.T) {
	st := open(t)

	if _, err := st.Put("Вложения/своя.png", []byte("свои байты"), "", "тест"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Put("Вложения/чужая.png", []byte("чужие байты"), "", "тест"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Put("Заметка.md",
		[]byte("# Заметка\n\nВот картинка: ![[Вложения/своя.png]]\n"), "", "тест"); err != nil {
		t.Fatal(err)
	}

	sh, err := st.CreateShare("Заметка.md", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, allowed, err := st.Share(sh.Key)
	if err != nil {
		t.Fatal(err)
	}

	own, _ := st.Hash("Вложения/своя.png")
	other, _ := st.Hash("Вложения/чужая.png")

	if len(allowed) != 1 {
		t.Fatalf("ожидал одно разрешённое вложение, получил %d: %v", len(allowed), allowed)
	}
	if allowed[0] != own {
		t.Errorf("разрешено не то вложение")
	}
	for _, h := range allowed {
		if h == other {
			t.Fatal("по ссылке открыто вложение из другой заметки")
		}
	}
}

func TestСсылкаНаНесуществующуюЗаметку(t *testing.T) {
	st := open(t)
	if _, err := st.CreateShare("Нет такой.md", time.Hour); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ожидал ErrNotFound, получил %v", err)
	}
}
