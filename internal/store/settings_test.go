package store_test

import (
	"testing"
)

func TestПорядокПапокСохраняется(t *testing.T) {
	st := open(t)

	if got, _ := st.RootOrder(); len(got) != 0 {
		t.Fatalf("пустое хранилище вернуло порядок: %v", got)
	}

	want := []string{"📓 Дневник", "📖 Go", "Ачивки"}
	if err := st.SetRootOrder(want); err != nil {
		t.Fatal(err)
	}
	got, err := st.RootOrder()
	if err != nil {
		t.Fatal(err)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("порядок исказился: %v", got)
		}
	}
}

// Список приходит из браузера, полагаться на него нельзя.
func TestПорядокЧиститсяОтМусора(t *testing.T) {
	st := open(t)

	if err := st.SetRootOrder([]string{"Первая", "", "Вторая", "Первая", "Третья", ""}); err != nil {
		t.Fatal(err)
	}
	got, _ := st.RootOrder()

	want := []string{"Первая", "Вторая", "Третья"}
	if len(got) != len(want) {
		t.Fatalf("повторы и пустые имена не убраны: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("на месте %d ожидал %q, получил %q", i, want[i], got[i])
		}
	}
}

// Испорченное значение не должно ломать показ дерева.
func TestИспорченныйПорядокНеЛомаетДерево(t *testing.T) {
	st := open(t)

	if err := st.SetSetting("root_order", "{это не список}"); err != nil {
		t.Fatal(err)
	}
	got, err := st.RootOrder()
	if err != nil {
		t.Fatalf("испорченное значение вернуло ошибку вместо пустого порядка: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ожидал пустой порядок, получил %v", got)
	}
}

func TestПорядокПереживаетПерезапуск(t *testing.T) {
	dir := t.TempDir()

	first := openAt(t, dir)
	if err := first.SetRootOrder([]string{"Раз", "Два"}); err != nil {
		t.Fatal(err)
	}
	first.Close()

	second := openAt(t, dir)
	got, _ := second.RootOrder()
	if len(got) != 2 || got[0] != "Раз" {
		t.Errorf("после перезапуска порядок потерян: %v", got)
	}
}
