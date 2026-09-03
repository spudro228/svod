package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Проверка на утечку: что сервер отдаёт тому, у кого ключа нет.
//
// Это не проверка «работает ли», а перечень всего, что не должно
// покидать свод. Если появится новая ручка, её надо добавить сюда.

// Единственные адреса, которым положено отвечать без ключа.
func TestБезКлючаДоступныТолькоРазрешённыеРучки(t *testing.T) {
	srv, owner := newServer(t, token)
	login(t, srv.URL, owner)
	put(t, srv.URL, owner, "Тайная.md", "# Тайная\n\nсовершенно секретный текст\n")
	put(t, srv.URL, owner, "Вложения/тайная.png", "секретные байты")

	closed := []string{
		"/api/v1/tree",
		"/api/v1/changes?since=0",
		"/api/v1/search?q=секретный",
		"/api/v1/tags",
		"/api/v1/note/Тайная.md",
		"/api/v1/raw/Тайная.md",
		"/api/v1/raw/Вложения/тайная.png",
		"/api/v1/history/Тайная.md",
		"/api/v1/share",
		"/api/v1/stream",
	}

	guest := &http.Client{}
	for _, path := range closed {
		res, err := guest.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		body := readAll(t, res)
		res.Body.Close()

		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s отдал %d вместо 401", path, res.StatusCode)
		}
		if strings.Contains(body, "совершенно секретный") || strings.Contains(body, "Тайная") {
			t.Errorf("%s протёк содержимым: %q", path, body)
		}
	}
}

// Блоб адресуется по хешу. Хеш не секрет, но и раздавать содержимое
// по нему без ключа нельзя: угадать нечего, а вот утечь может.
func TestБлобНеОтдаётсяБезКлюча(t *testing.T) {
	srv, owner := newServer(t, token)
	login(t, srv.URL, owner)
	put(t, srv.URL, owner, "Секрет.md", "# Секрет\n\nтекст под ключом\n")

	res, err := owner.Get(srv.URL + "/api/v1/note/Секрет.md")
	if err != nil {
		t.Fatal(err)
	}
	var note struct {
		Hash string `json:"hash"`
	}
	json.NewDecoder(res.Body).Decode(&note)
	res.Body.Close()

	guest := &http.Client{}
	gr, err := guest.Get(srv.URL + "/api/v1/blob/" + note.Hash)
	if err != nil {
		t.Fatal(err)
	}
	defer gr.Body.Close()
	if gr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("блоб отдан без ключа: %d", gr.StatusCode)
	}
}

// Ошибки не должны рассказывать о содержимом свода.
func TestОшибкиНеРаскрываютСуществованиеФайлов(t *testing.T) {
	srv, owner := newServer(t, token)
	login(t, srv.URL, owner)
	put(t, srv.URL, owner, "Существует.md", "# Существует\n")

	guest := &http.Client{}
	real := getStatus(t, guest, srv.URL+"/api/v1/note/Существует.md")
	fake := getStatus(t, guest, srv.URL+"/api/v1/note/Выдуманная.md")

	if real != fake {
		t.Errorf("по коду ответа видно, какие заметки существуют: %d и %d", real, fake)
	}
}

// Страница гостя не должна нести ничего, кроме одной заметки.
func TestСтраницаГостяНеСодержитЧужого(t *testing.T) {
	srv, owner := newServer(t, token)
	login(t, srv.URL, owner)
	put(t, srv.URL, owner, "Открытая.md", "# Открытая\n\nразрешено\n")
	put(t, srv.URL, owner, "Тайная.md", "# Тайная\n\nзапрещено\n")
	put(t, srv.URL, owner, "Ссылается.md", "# Ссылается\n\n[[Открытая]]\n")

	key, _ := share(t, srv.URL, owner, "Открытая.md", 24)
	page := fetchPage(t, srv.URL+"/s/"+key)

	for _, forbidden := range []string{"Тайная", "запрещено", "Ссылается"} {
		if strings.Contains(page, forbidden) {
			t.Errorf("на странице гостя оказалось чужое: %q", forbidden)
		}
	}
	if !strings.Contains(page, "разрешено") {
		t.Error("своей заметки на странице нет")
	}
}

// Токен не должен появляться нигде, кроме заголовков запроса.
func TestТокенНеПопадаетВОтветы(t *testing.T) {
	srv, owner := newServer(t, token)
	login(t, srv.URL, owner)
	put(t, srv.URL, owner, "Обычная.md", "# Обычная\n")
	key, _ := share(t, srv.URL, owner, "Обычная.md", 24)

	pages := []string{
		srv.URL + "/s/" + key,
		srv.URL + "/api/v1/shared/" + key,
	}
	guest := &http.Client{}
	for _, u := range pages {
		res, err := guest.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		body := readAll(t, res)
		res.Body.Close()
		if strings.Contains(body, token) {
			t.Errorf("%s содержит токен доступа", u)
		}
	}

	// И у владельца тоже: он ходит с токеном, но получать его обратно
	// в теле ответа незачем.
	res, err := owner.Get(srv.URL + "/api/v1/tree")
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, res)
	res.Body.Close()
	if strings.Contains(body, token) {
		t.Error("дерево свода содержит токен")
	}
}

func getStatus(t *testing.T, c *http.Client, url string) int {
	t.Helper()
	res, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	return res.StatusCode
}

func readAll(t *testing.T, res *http.Response) string {
	t.Helper()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := res.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return sb.String()
}
