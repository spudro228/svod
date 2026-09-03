package api_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spudro228/svod/internal/api"
	"github.com/spudro228/svod/internal/store"
)

const token = "секретный-токен-для-теста"

func newServer(t *testing.T, tok string) (*httptest.Server, *http.Client) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("хранилище: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	srv := httptest.NewServer(api.New(st, tok, nil, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)
	return srv, &http.Client{Jar: jar}
}

func TestБезТокенаНеПускает(t *testing.T) {
	srv, c := newServer(t, token)

	res, err := c.Get(srv.URL + "/api/v1/tree")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("ожидал 401, получил %d", res.StatusCode)
	}
}

func TestВходПоТокенуДаётКуку(t *testing.T) {
	srv, c := newServer(t, token)

	res, err := c.Post(srv.URL+"/api/v1/auth", "application/json",
		strings.NewReader(`{"token":"`+token+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("вход не удался: %d", res.StatusCode)
	}

	// В куке должен лежать не сам токен: её утечка не должна давать
	// ключ, которым ходят демоны.
	var found string
	for _, ck := range res.Cookies() {
		if ck.Name == "svod_session" {
			found = ck.Value
			if !ck.HttpOnly {
				t.Error("кука должна быть HttpOnly")
			}
		}
	}
	if found == "" {
		t.Fatal("куку не выдали")
	}
	if found == token {
		t.Error("в куке лежит сам токен")
	}

	// С кукой в банке доступ появляется.
	res2, err := c.Get(srv.URL + "/api/v1/tree")
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("с кукой ожидал 200, получил %d", res2.StatusCode)
	}
}

func TestНеверныйТокенОтвергается(t *testing.T) {
	srv, c := newServer(t, token)

	res, err := c.Post(srv.URL+"/api/v1/auth", "application/json",
		strings.NewReader(`{"token":"не тот"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("ожидал 401, получил %d", res.StatusCode)
	}
	if len(res.Cookies()) != 0 {
		t.Error("на неверный токен выдали куку")
	}
}

func TestВыходЗабираетДоступ(t *testing.T) {
	srv, c := newServer(t, token)

	if _, err := c.Post(srv.URL+"/api/v1/auth", "application/json",
		strings.NewReader(`{"token":"`+token+`"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Post(srv.URL+"/api/v1/logout", "application/json", nil); err != nil {
		t.Fatal(err)
	}

	res, err := c.Get(srv.URL + "/api/v1/tree")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("после выхода ожидал 401, получил %d", res.StatusCode)
	}
}

func TestЗаголовокAuthorizationРаботает(t *testing.T) {
	srv, _ := newServer(t, token)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/tree", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("демон должен ходить по заголовку, получил %d", res.StatusCode)
	}
}

func TestБезТокенаНаСервереВходНеТребуется(t *testing.T) {
	srv, c := newServer(t, "")

	res, err := c.Get(srv.URL + "/api/v1/auth")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	var st struct {
		Required   bool `json:"required"`
		Authorized bool `json:"authorized"`
	}
	if err := json.NewDecoder(res.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if st.Required || !st.Authorized {
		t.Fatalf("локальное демо не должно требовать вход: %+v", st)
	}
}
