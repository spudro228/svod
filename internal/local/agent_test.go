package local_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spudro228/svod/internal/api"
	"github.com/spudro228/svod/internal/local"
	"github.com/spudro228/svod/internal/store"
	"github.com/spudro228/svod/internal/watch"
)

// ───────────────────────── обвязка ─────────────────────────

func newServer(t *testing.T) string {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("хранилище: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(api.New(st, "", nil, nil, log))
	t.Cleanup(srv.Close)
	return srv.URL
}

func newAgent(t *testing.T, serverURL, device string) *local.Agent {
	t.Helper()
	vault := t.TempDir()
	state, err := local.Open(vault)
	if err != nil {
		t.Fatalf("состояние: %v", err)
	}
	t.Cleanup(func() { state.Close() })

	return &local.Agent{
		Vault:  vault,
		Server: serverURL,
		Token:  "",
		Device: device,
		State:  state,
		Ign:    watch.NewIgnore(vault, []string{".md"}),
		HC:     &http.Client{Timeout: 10 * time.Second},
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// sync — то же, что делает демон при запуске: сначала забрать, потом отдать.
func sync(t *testing.T, a *local.Agent) {
	t.Helper()
	ctx := context.Background()
	if err := a.Pull(ctx); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if err := a.SyncAll(ctx); err != nil {
		t.Fatalf("push: %v", err)
	}
}

func write(t *testing.T, a *local.Agent, rel, content string) {
	t.Helper()
	full := filepath.Join(a.Vault, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, a *local.Agent, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(a.Vault, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("не читается %s: %v", rel, err)
	}
	return string(b)
}

func list(t *testing.T, a *local.Agent) []string {
	t.Helper()
	files, err := watch.Scan(a.Vault, a.Ign)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// ───────────────────────── тесты ─────────────────────────

// Критерий этапа 2: два демона в разных папках сходятся к одному состоянию.
func TestДваДемонаСходятся(t *testing.T) {
	url := newServer(t)
	a := newAgent(t, url, "мак")
	b := newAgent(t, url, "пк")

	write(t, a, "Заметки/Первая.md", "# Первая\n\nтекст с мака\n")
	sync(t, a)

	sync(t, b)
	if got := read(t, b, "Заметки/Первая.md"); !strings.Contains(got, "текст с мака") {
		t.Fatalf("на пк не приехало содержимое: %q", got)
	}

	// Обратная дорога: правка на пк доезжает до мака.
	write(t, b, "Заметки/Первая.md", "# Первая\n\nдописано на пк\n")
	sync(t, b)
	sync(t, a)

	if got := read(t, a, "Заметки/Первая.md"); !strings.Contains(got, "дописано на пк") {
		t.Fatalf("на мак не вернулось: %q", got)
	}
}

// Демон не должен отправлять обратно то, что сам же принял.
func TestЭхоПогашено(t *testing.T) {
	url := newServer(t)
	a := newAgent(t, url, "мак")
	b := newAgent(t, url, "пк")

	write(t, a, "Эхо.md", "# Эхо\n")
	sync(t, a)
	sync(t, b) // b принимает файл с сервера

	seqAfterPull, err := b.State.LastSeq()
	if err != nil {
		t.Fatal(err)
	}

	// Повторные проходы не должны рождать новых версий.
	for range 3 {
		sync(t, b)
		sync(t, a)
	}

	seqNow, err := b.State.LastSeq()
	if err != nil {
		t.Fatal(err)
	}
	if seqNow != seqAfterPull {
		t.Fatalf("эхо не погашено: seq вырос с %d до %d", seqAfterPull, seqNow)
	}
}

// Главный критерий: одновременная правка даёт конфликтную копию,
// а не потерю текста.
func TestКонфликтСохраняетОбеВерсии(t *testing.T) {
	url := newServer(t)
	a := newAgent(t, url, "мак")
	b := newAgent(t, url, "пк")

	write(t, a, "Общий.md", "# Общий\n\nобщее начало\n")
	sync(t, a)
	sync(t, b)

	// Обе машины правят один файл, не видя друг друга.
	write(t, a, "Общий.md", "# Общий\n\nправка с мака\n")
	write(t, b, "Общий.md", "# Общий\n\nправка с пк\n")

	sync(t, a) // мак успевает первым
	sync(t, b) // пк получает 409

	if b.Conflicts() != 1 {
		t.Fatalf("ожидал один конфликт, получил %d", b.Conflicts())
	}

	// По каноническому пути — версия победителя.
	if got := read(t, b, "Общий.md"); !strings.Contains(got, "правка с мака") {
		t.Fatalf("на каноническом пути не версия сервера: %q", got)
	}

	// Проигравшая версия лежит рядом и ничего не потеряла.
	var copyPath string
	for _, f := range list(t, b) {
		if strings.Contains(f, "конфликт") {
			copyPath = f
		}
	}
	if copyPath == "" {
		t.Fatalf("конфликтная копия не создана, в папке: %v", list(t, b))
	}
	if got := read(t, b, copyPath); !strings.Contains(got, "правка с пк") {
		t.Fatalf("в конфликтной копии не тот текст: %q", got)
	}

	// Копия уезжает на сервер, и мак её тоже видит.
	sync(t, a)
	found := false
	for _, f := range list(t, a) {
		if strings.Contains(f, "конфликт") {
			found = true
		}
	}
	if !found {
		t.Fatalf("конфликтная копия не доехала до мака: %v", list(t, a))
	}
}

// Удаление на одной машине доезжает до другой.
func TestУдалениеРазъезжается(t *testing.T) {
	url := newServer(t)
	a := newAgent(t, url, "мак")
	b := newAgent(t, url, "пк")

	write(t, a, "Времянка.md", "# Времянка\n")
	sync(t, a)
	sync(t, b)

	if len(list(t, b)) != 1 {
		t.Fatalf("файл не приехал: %v", list(t, b))
	}

	if err := os.Remove(filepath.Join(a.Vault, "Времянка.md")); err != nil {
		t.Fatal(err)
	}
	sync(t, a)
	sync(t, b)

	if got := list(t, b); len(got) != 0 {
		t.Fatalf("файл не удалился на втором демоне: %v", got)
	}
}

// Файл, изменённый локально, не должен исчезнуть из-за удаления на сервере.
func TestУдалениеНеТеретЛокальнуюПравку(t *testing.T) {
	url := newServer(t)
	a := newAgent(t, url, "мак")
	b := newAgent(t, url, "пк")

	write(t, a, "Спорный.md", "# Спорный\n\nначало\n")
	sync(t, a)
	sync(t, b)

	// На маке удалили, на пк в это же время дописали.
	if err := os.Remove(filepath.Join(a.Vault, "Спорный.md")); err != nil {
		t.Fatal(err)
	}
	write(t, b, "Спорный.md", "# Спорный\n\nважная правка\n")

	sync(t, a)
	sync(t, b)

	got := read(t, b, "Спорный.md")
	if !strings.Contains(got, "важная правка") {
		t.Fatalf("локальная правка потеряна: %q", got)
	}
}

// Пути с кириллицей, пробелами и эмодзи должны ходить целыми.
func TestНепростыеПути(t *testing.T) {
	url := newServer(t)
	a := newAgent(t, url, "мак")
	b := newAgent(t, url, "пк")

	rel := "📖 Go/Ачивки 2026/Микросервис доставки — СДЭК.md"
	write(t, a, rel, "# СДЭК\n\nтег #go и ссылка [[Другая]]\n")
	sync(t, a)
	sync(t, b)

	if got := read(t, b, rel); !strings.Contains(got, "СДЭК") {
		t.Fatalf("путь с эмодзи и кириллицей не доехал: %q", got)
	}
}

func TestИмяКонфликтнойКопии(t *testing.T) {
	at := time.Date(2026, 9, 2, 14, 5, 0, 0, time.UTC)

	cases := []struct {
		in, want string
	}{
		{"Ачивки/2026_2/СДЭК.md", "Ачивки/2026_2/СДЭК (конфликт, мак, 2026-09-02 14:05).md"},
		{"Заметка.md", "Заметка (конфликт, мак, 2026-09-02 14:05).md"},
	}
	for _, c := range cases {
		if got := local.ConflictPath(c.in, "мак", at); got != c.want {
			t.Errorf("ConflictPath(%q):\n получил %q\n ожидал  %q", c.in, got, c.want)
		}
	}
}

// Переименование каталога файловая система показывает одним событием
// на сам каталог: на файлы внутри событий нет. Без обхода они остались бы
// на сервере под старым именем навсегда.
func TestПереименованиеКаталогаДоезжает(t *testing.T) {
	url := newServer(t)
	a := newAgent(t, url, "мак")
	b := newAgent(t, url, "пк")

	write(t, a, "Языки/Японский/Звуки.md", "# Звуки\n\nпроизношение\n")
	write(t, a, "Языки/Японский/Кандзи.md", "# Кандзи\n")
	sync(t, a)
	sync(t, b)

	if got := len(list(t, b)); got != 2 {
		t.Fatalf("файлы не приехали: %v", list(t, b))
	}

	// Переименовываем каталог целиком, как это делает файловый менеджер.
	oldDir := filepath.Join(a.Vault, "Языки", "Японский")
	newDir := filepath.Join(a.Vault, "Языки", "🈵 Японский")
	if err := os.Rename(oldDir, newDir); err != nil {
		t.Fatal(err)
	}

	sync(t, a)
	sync(t, b)

	got := list(t, b)
	if len(got) != 2 {
		t.Fatalf("после переименования каталога на втором демоне %d файлов: %v", len(got), got)
	}
	for _, f := range got {
		if !strings.Contains(f, "🈵 Японский") {
			t.Errorf("файл остался под старым путём: %s", f)
		}
	}

	// Старого каталога быть не должно ни на диске, ни на сервере.
	if _, err := os.Stat(filepath.Join(b.Vault, "Языки", "Японский")); err == nil {
		t.Error("старый каталог не удалился на втором демоне")
	}
}
