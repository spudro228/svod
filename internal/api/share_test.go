package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Временные ссылки — единственное место, где свод отвечает без ключа.
// Поэтому здесь проверяется не «работает ли», а «не даёт ли лишнего».

func share(t *testing.T, srv string, c *http.Client, path string, hours int) (key, url string) {
	t.Helper()
	body := `{"path":"` + path + `","hours":` + itoa(hours) + `}`
	res, err := c.Post(srv+"/api/v1/share", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("ссылку не выдали: %d", res.StatusCode)
	}
	var sh struct {
		Key string `json:"key"`
		URL string `json:"url"`
	}
	if err := json.NewDecoder(res.Body).Decode(&sh); err != nil {
		t.Fatal(err)
	}
	return sh.Key, sh.URL
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func put(t *testing.T, srv string, c *http.Client, path, content string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, srv+"/api/v1/files/"+path, strings.NewReader(content))
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("не залил %s: %d", path, res.StatusCode)
	}
}

func login(t *testing.T, srv string, c *http.Client) {
	t.Helper()
	res, err := c.Post(srv+"/api/v1/auth", "application/json",
		strings.NewReader(`{"token":"`+token+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
}

func TestСсылкаОткрываетЗаметкуБезТокена(t *testing.T) {
	srv, owner := newServer(t, token)
	login(t, srv.URL, owner)
	put(t, srv.URL, owner, "Открытая.md", "# Открытая\n\nэто можно показать\n")

	key, url := share(t, srv.URL, owner, "Открытая.md", 24)
	if !strings.HasSuffix(url, "/s/"+key) {
		t.Errorf("странный адрес ссылки: %q", url)
	}

	// Гость: свой клиент, без куки и без заголовка.
	guest := &http.Client{}
	res, err := guest.Get(srv.URL + "/api/v1/shared/" + key)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("гостя не пустили: %d", res.StatusCode)
	}

	var out struct {
		Note struct {
			Content   string   `json:"content"`
			Backlinks []string `json:"backlinks"`
		} `json:"note"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Note.Content, "это можно показать") {
		t.Errorf("содержимое не отдали: %q", out.Note.Content)
	}
	if res.Header.Get("X-Robots-Tag") == "" {
		t.Error("нет запрета на индексацию")
	}
}

// Главная проверка: ссылка на одну заметку не открывает другую.
func TestСсылкаНеОткрываетСоседнююЗаметку(t *testing.T) {
	srv, owner := newServer(t, token)
	login(t, srv.URL, owner)
	put(t, srv.URL, owner, "Открытая.md", "# Открытая\n")
	put(t, srv.URL, owner, "Тайная.md", "# Тайная\n\nсекрет\n")

	key, _ := share(t, srv.URL, owner, "Открытая.md", 24)
	guest := &http.Client{}

	// Ни одна из обычных ручек не должна пустить гостя.
	for _, path := range []string{
		"/api/v1/note/Тайная.md",
		"/api/v1/raw/Тайная.md",
		"/api/v1/tree",
		"/api/v1/search?q=секрет",
		"/api/v1/changes?since=0",
		"/api/v1/history/Тайная.md",
	} {
		res, err := guest.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s отдал %d вместо 401", path, res.StatusCode)
		}
	}

	// И через саму ссылку до чужой заметки не добраться:
	// путь в гостевой ручке не параметр, а запись в строке ссылки.
	res, err := guest.Get(srv.URL + "/api/v1/shared/" + key + "/asset/Тайная.md")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("через вложения открылась чужая заметка: %d", res.StatusCode)
	}
}

func TestОтозваннаяСсылкаПересаётРаботать(t *testing.T) {
	srv, owner := newServer(t, token)
	login(t, srv.URL, owner)
	put(t, srv.URL, owner, "Отзыв.md", "# Отзыв\n")

	key, _ := share(t, srv.URL, owner, "Отзыв.md", 24)
	guest := &http.Client{}

	res, err := guest.Get(srv.URL + "/api/v1/shared/" + key)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("ссылка не работала с самого начала: %d", res.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/share/"+key, nil)
	dr, derr := owner.Do(req)
	if derr != nil {
		t.Fatal(derr)
	}
	dr.Body.Close()

	res2, err := guest.Get(srv.URL + "/api/v1/shared/" + key)
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if res2.StatusCode != http.StatusNotFound {
		t.Errorf("после отзыва ссылка ещё жива: %d", res2.StatusCode)
	}
}

// Отозванная и несуществующая отвечают одинаково: иначе по разнице
// ответов можно выяснять, какие ключи когда-то выдавались.
// Истечение по сроку проверяется в store: там его можно задать в прошлом.
func TestОтозваннаяНеотличимаОтНесуществующей(t *testing.T) {
	srv, owner := newServer(t, token)
	login(t, srv.URL, owner)
	put(t, srv.URL, owner, "Неразличимость.md", "# Неразличимость\n")

	key, _ := share(t, srv.URL, owner, "Неразличимость.md", 24)
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/share/"+key, nil)
	if res, err := owner.Do(req); err == nil {
		res.Body.Close()
	}

	guest := &http.Client{}
	revoked, err := guest.Get(srv.URL + "/api/v1/shared/" + key)
	if err != nil {
		t.Fatal(err)
	}
	defer revoked.Body.Close()
	missing, err := guest.Get(srv.URL + "/api/v1/shared/совершенно-выдуманный-ключ")
	if err != nil {
		t.Fatal(err)
	}
	defer missing.Body.Close()

	if revoked.StatusCode != missing.StatusCode {
		t.Errorf("отозванная отвечает %d, несуществующая %d — по разнице можно перебирать ключи",
			revoked.StatusCode, missing.StatusCode)
	}
}

func TestСписокСсылокТребуетТокена(t *testing.T) {
	srv, owner := newServer(t, token)
	login(t, srv.URL, owner)
	put(t, srv.URL, owner, "Список.md", "# Список\n")
	share(t, srv.URL, owner, "Список.md", 24)

	guest := &http.Client{}
	res, err := guest.Get(srv.URL + "/api/v1/share")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("гость видит список выданных ссылок: %d", res.StatusCode)
	}

	own, err := owner.Get(srv.URL + "/api/v1/share")
	if err != nil {
		t.Fatal(err)
	}
	defer own.Body.Close()
	var out struct {
		Shares []struct {
			Path string `json:"path"`
		} `json:"shares"`
	}
	if err := json.NewDecoder(own.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Shares) != 1 || out.Shares[0].Path != "Список.md" {
		t.Errorf("владелец не видит свою ссылку: %+v", out.Shares)
	}
}

func TestКлючиНеПовторяютсяИНеПредсказуемы(t *testing.T) {
	srv, owner := newServer(t, token)
	login(t, srv.URL, owner)
	put(t, srv.URL, owner, "Ключи.md", "# Ключи\n")

	seen := map[string]bool{}
	for range 5 {
		key, _ := share(t, srv.URL, owner, "Ключи.md", 24)
		if seen[key] {
			t.Fatal("ключ повторился")
		}
		if len(key) < 32 {
			t.Fatalf("ключ слишком короткий: %d символов", len(key))
		}
		seen[key] = true
	}
}
