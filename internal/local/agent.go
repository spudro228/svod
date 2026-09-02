// Движок синхронизации на стороне машины.
//
// Три правила, на которых всё держится:
//
//  1. Атомарность. Файл всегда пишется во временный файл рядом и переносится
//     через rename. Иначе редактор прочитает половину.
//
//  2. Гашение эха. Состояние обновляется ДО rename. Демон сам пишет файлы,
//     приехавшие с сервера, и fsnotify тут же о них сообщит; если не запомнить
//     ожидаемый хеш заранее, изменение уедет обратно и два демона будут гонять
//     его по кругу вечно.
//
//  3. Ничего не теряем. Если обе стороны правили один файл, серверная версия
//     ложится по каноническому пути, а локальная — рядом отдельным файлом.
package local

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/spudro228/svod/internal/proto"
	"github.com/spudro228/svod/internal/store"
	"github.com/spudro228/svod/internal/watch"
)

// PollInterval — страховка на случай, если WebSocket отвалился незаметно.
const PollInterval = 30 * time.Second

// DefaultExts — что синхронизируем по умолчанию: заметки и вложения к ним.
const DefaultExts = ".md,.png,.jpg,.jpeg,.gif,.webp,.svg,.pdf"

type Agent struct {
	Vault  string
	Server string
	Token  string
	Device string

	State *State
	Ign   *watch.Ignore
	HC    *http.Client
	Log   *slog.Logger

	// Синхронизация идёт по одному файлу за раз: приезжающие и уезжающие
	// изменения не должны пересекаться на одном пути.
	mu sync.Mutex

	conflicts int
}

// Conflicts — сколько конфликтов демон развёл с момента запуска.
func (a *Agent) Conflicts() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.conflicts
}

// ───────────────────────── дорога вниз ─────────────────────────

// Pull забирает с сервера всё, что произошло после известного нам seq.
func (a *Agent) Pull(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	since, err := a.State.LastSeq()
	if err != nil {
		return err
	}

	res, err := a.do(ctx, http.MethodGet, "/api/v1/changes?since="+fmt.Sprint(since), nil, nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("changes: %s: %s", res.Status, readErr(res.Body))
	}

	var ch proto.Changes
	if err := json.NewDecoder(res.Body).Decode(&ch); err != nil {
		return err
	}
	if len(ch.Changes) == 0 {
		return a.State.SetLastSeq(ch.Seq)
	}

	var applied int
	for _, c := range ch.Changes {
		if a.Ign.Match(c.Path) {
			continue // на этой машине такие файлы не синхронизируем
		}
		if err := a.apply(ctx, c); err != nil {
			a.Log.Warn("не смог применить изменение", "path", c.Path, "err", err)
			continue
		}
		applied++
	}
	if applied > 0 {
		a.Log.Info("забрал с сервера", "изменений", applied, "seq", ch.Seq)
	}
	return a.State.SetLastSeq(ch.Seq)
}

// apply раскладывает одно изменение с сервера на диск.
func (a *Agent) apply(ctx context.Context, c proto.FileMeta) error {
	full := filepath.Join(a.Vault, filepath.FromSlash(c.Path))

	known, err := a.State.Hash(c.Path)
	if err != nil {
		return err
	}

	// Что сейчас лежит на диске.
	diskContent, readErr := os.ReadFile(full)
	diskHash := ""
	if readErr == nil {
		diskHash = store.HashOf(diskContent)
	}

	// Сервер стоит ровно на той версии, от которой мы отталкиваемся.
	// Значит, приезжать нечему: всё, что отличается на диске, — наша
	// собственная неотправленная правка (или наше же удаление),
	// и она уедет наверх ближайшим push. Без этой проверки демон
	// принимает свои изменения за чужие и разводит ложный конфликт.
	if !c.Deleted && c.Hash == known {
		return nil
	}

	if c.Deleted {
		if diskHash == "" {
			return a.State.Forget(c.Path)
		}
		if diskHash != known {
			// На сервере удалили, а у нас правили. Удалять нельзя:
			// забываем путь, и он уедет обратно как новый файл.
			a.Log.Warn("на сервере удалён, локально изменён — оставляю файл", "path", c.Path)
			return a.State.Forget(c.Path)
		}
		if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
			return err
		}
		a.Log.Info("удалён с сервера", "path", c.Path)
		return a.State.Forget(c.Path)
	}

	if diskHash == c.Hash {
		// Уже нужная версия — возможно, мы сами её и залили.
		return a.State.Set(c.Path, c.Hash)
	}

	content, err := a.blob(ctx, c.Hash)
	if err != nil {
		return err
	}

	// Локальные правки поверх версии, которую сервер уже обогнал.
	if diskHash != "" && diskHash != known {
		return a.splitConflict(ctx, c.Path, diskContent, content, c.Hash)
	}

	if err := a.writeFromServer(c.Path, content, c.Hash); err != nil {
		return err
	}
	a.Log.Info("принят с сервера", "path", c.Path)
	return nil
}

// splitConflict разводит две версии одного файла по разным путям.
// Серверная остаётся на каноническом пути, локальная ложится рядом.
func (a *Agent) splitConflict(ctx context.Context, rel string, mine, theirs []byte, theirHash string) error {
	copyPath := ConflictPath(rel, a.Device, time.Now())

	// Сначала спасаем свою версию, только потом трогаем оригинал.
	if err := a.writeFile(copyPath, mine); err != nil {
		return fmt.Errorf("не смог сохранить свою версию: %w", err)
	}
	if err := a.writeFromServer(rel, theirs, theirHash); err != nil {
		return err
	}

	a.conflicts++
	a.Log.Warn("конфликт — обе версии сохранены",
		"путь", rel, "твоя версия", copyPath)

	// Конфликтная копия уезжает на сервер как обычный новый файл.
	if _, err := a.pushLocked(ctx, copyPath); err != nil {
		a.Log.Warn("конфликтную копию не залил", "path", copyPath, "err", err)
	}
	return nil
}

// ConflictPath строит имя для проигравшей версии.
func ConflictPath(rel, device string, at time.Time) string {
	dir := path.Dir(rel)
	base := path.Base(rel)
	ext := path.Ext(base)
	name := strings.TrimSuffix(base, ext)

	suffix := fmt.Sprintf(" (конфликт, %s, %s)", device, at.Format("2006-01-02 15:04"))
	out := name + suffix + ext
	if dir == "." || dir == "" {
		return out
	}
	return dir + "/" + out
}

// ───────────────────────── дорога наверх ─────────────────────────

// Push заливает файл, если его содержимое изменилось.
func (a *Agent) Push(ctx context.Context, rel string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pushLocked(ctx, rel)
}

func (a *Agent) pushLocked(ctx context.Context, rel string) (bool, error) {
	content, err := os.ReadFile(filepath.Join(a.Vault, filepath.FromSlash(rel)))
	if err != nil {
		return false, err
	}
	hash := store.HashOf(content)

	known, err := a.State.Hash(rel)
	if err != nil {
		return false, err
	}
	if known == hash {
		return false, nil // это и есть погашенное эхо
	}

	hdr := http.Header{}
	if known != "" {
		hdr.Set(proto.HeaderIfMatch, known)
	}

	res, err := a.do(ctx, http.MethodPut, "/api/v1/files/"+escapePath(rel), bytes.NewReader(content), hdr)
	if err != nil {
		return false, err
	}
	defer res.Body.Close()

	switch res.StatusCode {
	case http.StatusOK:
		var out proto.PutResult
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			return false, err
		}
		return true, a.State.Set(rel, out.Hash)

	case http.StatusConflict:
		var c proto.Conflict
		if err := json.NewDecoder(res.Body).Decode(&c); err != nil {
			return false, err
		}
		theirs, err := a.blob(ctx, c.ServerHash)
		if err != nil {
			return false, fmt.Errorf("не смог забрать серверную версию: %w", err)
		}
		return false, a.splitConflict(ctx, rel, content, theirs, c.ServerHash)

	default:
		return false, fmt.Errorf("сервер ответил %s: %s", res.Status, readErr(res.Body))
	}
}

// Remove отправляет надгробие: файла на диске больше нет.
func (a *Agent) Remove(ctx context.Context, rel string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	known, err := a.State.Hash(rel)
	if err != nil {
		return err
	}
	hdr := http.Header{}
	if known != "" {
		hdr.Set(proto.HeaderIfMatch, known)
	}

	res, err := a.do(ctx, http.MethodDelete, "/api/v1/files/"+escapePath(rel), nil, hdr)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	switch res.StatusCode {
	case http.StatusOK, http.StatusNotFound:
		return a.State.Forget(rel)
	case http.StatusConflict:
		// Файл успели изменить на сервере — удаление отменяется,
		// изменение приедет к нам следующим Pull.
		a.Log.Warn("удаление отменено: на сервере файл изменили", "path", rel)
		return a.State.Forget(rel)
	default:
		return fmt.Errorf("сервер ответил %s: %s", res.Status, readErr(res.Body))
	}
}

// SyncAll сверяет всю папку с известным состоянием и досылает разницу.
func (a *Agent) SyncAll(ctx context.Context) error {
	files, err := watch.Scan(a.Vault, a.Ign)
	if err != nil {
		return err
	}
	sort.Strings(files)

	known, err := a.State.All()
	if err != nil {
		return err
	}

	start := time.Now()
	var pushed, skipped, failed int
	seen := map[string]bool{}

	for _, rel := range files {
		seen[rel] = true
		changed, err := a.Push(ctx, rel)
		switch {
		case err != nil:
			failed++
			a.Log.Warn("не залил", "path", rel, "err", err)
		case changed:
			pushed++
		default:
			skipped++
		}
	}

	var removed int
	for rel := range known {
		if seen[rel] {
			continue
		}
		if err := a.Remove(ctx, rel); err != nil {
			a.Log.Warn("не удалил на сервере", "path", rel, "err", err)
			continue
		}
		removed++
	}

	a.Log.Info("синхронизация закончена",
		"всего", len(files), "залито", pushed, "без изменений", skipped,
		"удалено", removed, "ошибок", failed, "за", time.Since(start).Round(time.Millisecond))
	return nil
}

// SyncOne обрабатывает одно событие файловой системы.
func (a *Agent) SyncOne(ctx context.Context, rel string) error {
	if _, err := os.Stat(filepath.Join(a.Vault, filepath.FromSlash(rel))); os.IsNotExist(err) {
		known, _ := a.State.Hash(rel)
		if known == "" {
			return nil
		}
		a.Log.Info("удалён", "path", rel)
		return a.Remove(ctx, rel)
	}
	changed, err := a.Push(ctx, rel)
	if err == nil && changed {
		a.Log.Info("залит", "path", rel)
	}
	return err
}

// ───────────────────────── запись на диск ─────────────────────────

// writeFromServer кладёт приехавшую версию и гасит собственное эхо.
//
// Состояние обновляется до rename: к моменту, когда fsnotify сообщит о записи,
// демон уже знает этот хеш и не отправит файл обратно.
func (a *Agent) writeFromServer(rel string, content []byte, hash string) error {
	prev, err := a.State.Hash(rel)
	if err != nil {
		return err
	}
	if err := a.State.Set(rel, hash); err != nil {
		return err
	}
	if err := a.writeFile(rel, content); err != nil {
		// Записать не вышло — откатываем состояние, иначе демон решит,
		// что на диске лежит то, чего там нет.
		if prev == "" {
			_ = a.State.Forget(rel)
		} else {
			_ = a.State.Set(rel, prev)
		}
		return err
	}
	return nil
}

// writeFile пишет атомарно: временный файл рядом и rename.
func (a *Agent) writeFile(rel string, content []byte) error {
	full := filepath.Join(a.Vault, filepath.FromSlash(rel))
	dir := filepath.Dir(full)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// Временный файл обязан лежать в том же каталоге: rename атомарен
	// только в пределах одной файловой системы.
	tmp, err := os.CreateTemp(dir, ".svod-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, full); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// ───────────────────────── поток изменений ─────────────────────────

// Stream держит подписку на события сервера и дёргает onEvent на каждый seq.
// Соединение переустанавливается само; периодический Pull страхует на случай,
// когда сокет отвалился незаметно.
func (a *Agent) Stream(ctx context.Context, onEvent func()) {
	wsURL := strings.Replace(strings.Replace(a.Server, "https://", "wss://", 1), "http://", "ws://", 1)
	wsURL += "/api/v1/stream"

	hdr := http.Header{}
	if a.Token != "" {
		hdr.Set("Authorization", "Bearer "+a.Token)
	}

	backoff := time.Second
	for ctx.Err() == nil {
		c, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: hdr})
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}

		a.Log.Info("подписался на изменения сервера")
		backoff = time.Second

		// Между последним Pull и установкой подписки есть окно, и всё,
		// что произошло в нём, не придёт событием — его никто не услышал.
		// Поэтому сразу после подписки догоняемся один раз.
		onEvent()

		for ctx.Err() == nil {
			var ev proto.StreamEvent
			if err := wsjson.Read(ctx, c, &ev); err != nil {
				break
			}
			onEvent()
		}
		c.CloseNow()
		if ctx.Err() == nil {
			a.Log.Warn("связь с сервером потеряна, переподключаюсь")
		}
	}
}

// ───────────────────────── HTTP ─────────────────────────

func (a *Agent) blob(ctx context.Context, hash string) ([]byte, error) {
	res, err := a.do(ctx, http.MethodGet, "/api/v1/blob/"+hash, nil, nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("blob %s: %s", short(hash), res.Status)
	}
	content, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	// Содержимое адресуется по хешу — проверяем, что дали именно то.
	if got := store.HashOf(content); got != hash {
		return nil, fmt.Errorf("блоб испорчен: просили %s, получили %s", short(hash), short(got))
	}
	return content, nil
}

func (a *Agent) do(ctx context.Context, method, p string, body *bytes.Reader, hdr http.Header) (*http.Response, error) {
	var req *http.Request
	var err error
	if body == nil {
		req, err = http.NewRequestWithContext(ctx, method, a.Server+p, nil)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, a.Server+p, body)
	}
	if err != nil {
		return nil, err
	}
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if a.Token != "" {
		req.Header.Set("Authorization", "Bearer "+a.Token)
	}
	req.Header.Set(proto.HeaderDevice, a.Device)
	if body != nil {
		req.Header.Set("Content-Type", "text/markdown; charset=utf-8")
	}
	return a.HC.Do(req)
}

// escapePath кодирует каждый сегмент отдельно: разделители остаются
// разделителями, а кириллица, пробелы и эмодзи уезжают в проценты.
func escapePath(rel string) string {
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

func readErr(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 512))
	return strings.TrimSpace(string(b))
}

func short(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}
