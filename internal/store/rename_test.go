package store_test

import (
	"testing"

	"github.com/spudro228/svod/internal/store"
)

// Демон присылает переименование как удаление плюс создание, и порядок
// событий от файловой системы не гарантирован. Связь восстанавливается
// по совпадению содержимого: у файла с тем же хешем, появившегося рядом
// с исчезнувшим, другого объяснения нет.

func TestИсторияПереживаетПереименование(t *testing.T) {
	st := open(t)
	content := []byte("# Заметка\n\nпервая версия\n")

	// Две версии под старым именем.
	r1, err := st.Put("Старое имя.md", content, "", "мак")
	if err != nil {
		t.Fatal(err)
	}
	second := []byte("# Заметка\n\nвторая версия\n")
	if _, err := st.Put("Старое имя.md", second, r1.Hash, "мак"); err != nil {
		t.Fatal(err)
	}

	// Переименование: сначала удаление, потом создание.
	cur, _ := st.Hash("Старое имя.md")
	if _, err := st.Delete("Старое имя.md", cur, "мак"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Put("Новое имя.md", second, "", "мак"); err != nil {
		t.Fatal(err)
	}

	hist, err := st.History("Новое имя.md", 100)
	if err != nil {
		t.Fatal(err)
	}
	// Версия под новым именем плюс всё, что было под старым.
	if len(hist) < 4 {
		t.Fatalf("история оборвалась на переименовании: %d записей", len(hist))
	}

	var underOld int
	for _, v := range hist {
		if v.Path == "Старое имя.md" {
			underOld++
		}
	}
	if underOld == 0 {
		t.Error("в истории не видно прежнего имени")
	}
}

// Порядок событий может быть и обратным: новый файл приезжает раньше,
// чем удаление старого.
func TestПереименованиеВОбратномПорядке(t *testing.T) {
	st := open(t)
	content := []byte("# Заметка\n\nтекст\n")

	if _, err := st.Put("Было.md", content, "", "мак"); err != nil {
		t.Fatal(err)
	}
	// Создание нового пути приходит первым.
	if _, err := st.Put("Стало.md", content, "", "мак"); err != nil {
		t.Fatal(err)
	}
	cur, _ := st.Hash("Было.md")
	if _, err := st.Delete("Было.md", cur, "мак"); err != nil {
		t.Fatal(err)
	}

	hist, err := st.History("Стало.md", 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range hist {
		if v.Path == "Было.md" {
			found = true
		}
	}
	if !found {
		t.Errorf("при обратном порядке событий история не связалась: %+v", hist)
	}
}

// Копия — не переименование: исходный файл остался на месте,
// и приписывать копии его историю неверно.
func TestКопияНеСчитаетсяПереименованием(t *testing.T) {
	st := open(t)
	content := []byte("# Исходник\n\nтекст\n")

	if _, err := st.Put("Исходник.md", content, "", "мак"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Put("Копия.md", content, "", "мак"); err != nil {
		t.Fatal(err)
	}

	hist, err := st.History("Копия.md", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range hist {
		if v.Path == "Исходник.md" {
			t.Fatal("копии приписана история исходника, хотя тот никуда не делся")
		}
	}
	if len(hist) != 1 {
		t.Errorf("у копии должна быть одна своя версия, а их %d", len(hist))
	}
}

// Разное содержимое — разные файлы, даже если одно удалили, а другое создали.
func TestРазныеФайлыНеСвязываются(t *testing.T) {
	st := open(t)

	if _, err := st.Put("Первый.md", []byte("# Первый\n"), "", "мак"); err != nil {
		t.Fatal(err)
	}
	cur, _ := st.Hash("Первый.md")
	if _, err := st.Delete("Первый.md", cur, "мак"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Put("Второй.md", []byte("# Второй, совсем другой\n"), "", "мак"); err != nil {
		t.Fatal(err)
	}

	hist, err := st.History("Второй.md", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range hist {
		if v.Path == "Первый.md" {
			t.Fatal("связаны файлы с разным содержимым")
		}
	}
}

// Цепочка переименований разматывается до самого начала.
func TestЦепочкаПереименований(t *testing.T) {
	st := open(t)
	content := []byte("# Кочующая заметка\n")

	names := []string{"Первое.md", "Второе.md", "Третье.md"}
	if _, err := st.Put(names[0], content, "", "мак"); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(names); i++ {
		cur, _ := st.Hash(names[i-1])
		if _, err := st.Delete(names[i-1], cur, "мак"); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Put(names[i], content, "", "мак"); err != nil {
			t.Fatal(err)
		}
	}

	hist, err := st.History("Третье.md", 100)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, v := range hist {
		seen[v.Path] = true
	}
	for _, want := range []string{"Первое.md", "Второе.md"} {
		if !seen[want] {
			t.Errorf("в истории потеряно имя %s: %+v", want, hist)
		}
	}
}

var _ = store.ErrNotFound

// История читается сверху вниз, поэтому порядок должен быть строгим:
// звенья цепочки собираются по одному, и без сортировки версия старого
// имени оказывалась выше более новой.
func TestИсторияОтсортированаПоНомеру(t *testing.T) {
	st := open(t)
	content := []byte("# Кочующая\n")

	if _, err := st.Put("Раз.md", content, "", "мак"); err != nil {
		t.Fatal(err)
	}
	for _, step := range [][2]string{{"Раз.md", "Два.md"}, {"Два.md", "Три.md"}} {
		cur, _ := st.Hash(step[0])
		if _, err := st.Delete(step[0], cur, "мак"); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Put(step[1], content, "", "мак"); err != nil {
			t.Fatal(err)
		}
	}

	hist, err := st.History("Три.md", 100)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(hist); i++ {
		if hist[i-1].Seq < hist[i].Seq {
			t.Fatalf("история не по порядку: seq %d выше %d", hist[i-1].Seq, hist[i].Seq)
		}
	}
}
