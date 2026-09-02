// Команда svod — демон и CLI на машине.
//
// Следит за папкой свода, считает хеши и заливает изменения на сервер.
// Локальные файлы остаются источником правды: демон их только читает.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spudro228/svod/internal/local"
	"github.com/spudro228/svod/internal/proto"
	"github.com/spudro228/svod/internal/store"
	"github.com/spudro228/svod/internal/watch"
)

type daemon struct {
	vault  string
	server string
	token  string
	device string

	st  *local.State
	ig  *watch.Ignore
	hc  *http.Client
	log *slog.Logger
}

func main() {
	var (
		vault  = flag.String("vault", env("SVOD_VAULT", "."), "папка свода")
		server = flag.String("server", env("SVOD_SERVER", "http://localhost:8080"), "адрес сервера")
		token  = flag.String("token", os.Getenv("SVOD_TOKEN"), "токен доступа")
		device = flag.String("device", env("SVOD_DEVICE", hostname()), "имя устройства для истории версий")
		once   = flag.Bool("once", false, "синхронизировать один раз и выйти")
		status = flag.Bool("status", false, "показать состояние и выйти")
		exts   = flag.String("ext", ".md", "расширения через запятую; пусто — все файлы")
	)
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	root, err := filepath.Abs(*vault)
	if err != nil {
		log.Error("плохой путь до свода", "err", err)
		os.Exit(1)
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		log.Error("свод не найден", "vault", root)
		os.Exit(1)
	}

	var extList []string
	for _, e := range strings.Split(*exts, ",") {
		if e = strings.TrimSpace(e); e != "" {
			extList = append(extList, e)
		}
	}

	st, err := local.Open(root)
	if err != nil {
		log.Error("не смог открыть состояние", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	d := &daemon{
		vault:  root,
		server: strings.TrimSuffix(*server, "/"),
		token:  *token,
		device: *device,
		st:     st,
		ig:     watch.NewIgnore(root, extList),
		hc:     &http.Client{Timeout: 30 * time.Second},
		log:    log,
	}

	if *status {
		d.printStatus()
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("свод", "vault", root, "server", d.server, "device", d.device)

	if err := d.syncAll(ctx); err != nil {
		log.Error("первичная синхронизация не удалась", "err", err)
		if *once {
			os.Exit(1)
		}
	}
	if *once {
		return
	}

	w := watch.NewWatcher(root, d.ig, log)
	log.Info("слежу за папкой; Ctrl+C чтобы остановить")

	if err := w.Run(ctx, func(rel string) {
		if err := d.syncOne(ctx, rel); err != nil {
			log.Warn("не смог синхронизировать", "path", rel, "err", err)
		}
	}); err != nil {
		log.Error("слежение оборвалось", "err", err)
	}
}

// syncAll сверяет всю папку с известным состоянием и досылает разницу.
func (d *daemon) syncAll(ctx context.Context) error {
	files, err := watch.Scan(d.vault, d.ig)
	if err != nil {
		return err
	}
	sort.Strings(files)

	known, err := d.st.All()
	if err != nil {
		return err
	}

	start := time.Now()
	var pushed, skipped, failed int
	seen := map[string]bool{}

	for _, rel := range files {
		seen[rel] = true
		changed, err := d.push(ctx, rel)
		switch {
		case err != nil:
			failed++
			d.log.Warn("не залил", "path", rel, "err", err)
		case changed:
			pushed++
		default:
			skipped++
		}
	}

	// Пути, которые демон знал, а на диске их больше нет.
	var removed int
	for rel := range known {
		if seen[rel] {
			continue
		}
		if err := d.remove(ctx, rel); err != nil {
			d.log.Warn("не удалил на сервере", "path", rel, "err", err)
			continue
		}
		removed++
	}

	d.log.Info("синхронизация закончена",
		"всего", len(files), "залито", pushed, "без изменений", skipped,
		"удалено", removed, "ошибок", failed, "за", time.Since(start).Round(time.Millisecond))
	return nil
}

// syncOne обрабатывает одно событие файловой системы.
func (d *daemon) syncOne(ctx context.Context, rel string) error {
	if _, err := os.Stat(filepath.Join(d.vault, rel)); os.IsNotExist(err) {
		known, _ := d.st.Hash(rel)
		if known == "" {
			return nil // файла и не было
		}
		d.log.Info("удалён", "path", rel)
		return d.remove(ctx, rel)
	}
	changed, err := d.push(ctx, rel)
	if err == nil && changed {
		d.log.Info("залит", "path", rel)
	}
	return err
}

// push заливает файл, если его содержимое изменилось.
// Возвращает true, если что-то реально уехало на сервер.
func (d *daemon) push(ctx context.Context, rel string) (bool, error) {
	content, err := os.ReadFile(filepath.Join(d.vault, rel))
	if err != nil {
		return false, err
	}
	hash := store.HashOf(content)

	known, err := d.st.Hash(rel)
	if err != nil {
		return false, err
	}
	if known == hash {
		return false, nil
	}

	req, err := d.request(ctx, http.MethodPut, "/api/v1/files/"+escapePath(rel), bytes.NewReader(content))
	if err != nil {
		return false, err
	}
	// Пустой known означает «на сервере этого файла ещё нет»,
	// и тогда заголовок не отправляется вовсе.
	if known != "" {
		req.Header.Set(proto.HeaderIfMatch, known)
	}

	res, err := d.hc.Do(req)
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
		return true, d.st.Set(rel, out.Hash)

	case http.StatusConflict:
		var c proto.Conflict
		_ = json.NewDecoder(res.Body).Decode(&c)
		// Ничего не перезаписываем: на сервере чужая версия.
		// Раскладывание конфликтных копий появится вместе с обратной
		// синхронизацией — до неё файл просто остаётся незалитым.
		d.log.Warn("конфликт: на сервере другая версия",
			"path", rel, "сервер", short(c.ServerHash), "локально", short(hash))
		return false, nil

	default:
		return false, fmt.Errorf("сервер ответил %s: %s", res.Status, readErr(res.Body))
	}
}

// remove отправляет надгробие: файла на диске больше нет.
func (d *daemon) remove(ctx context.Context, rel string) error {
	known, err := d.st.Hash(rel)
	if err != nil {
		return err
	}
	req, err := d.request(ctx, http.MethodDelete, "/api/v1/files/"+escapePath(rel), nil)
	if err != nil {
		return err
	}
	if known != "" {
		req.Header.Set(proto.HeaderIfMatch, known)
	}

	res, err := d.hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	switch res.StatusCode {
	case http.StatusOK, http.StatusNotFound:
		return d.st.Forget(rel)
	case http.StatusConflict:
		d.log.Warn("конфликт при удалении — файл на сервере успели изменить", "path", rel)
		return nil
	default:
		return fmt.Errorf("сервер ответил %s: %s", res.Status, readErr(res.Body))
	}
}

func (d *daemon) printStatus() {
	files, err := watch.Scan(d.vault, d.ig)
	if err != nil {
		fmt.Printf("свод %s — не смог обойти: %v\n", d.vault, err)
		return
	}
	known, _ := d.st.All()

	var pending int
	for _, rel := range files {
		content, err := os.ReadFile(filepath.Join(d.vault, rel))
		if err != nil {
			continue
		}
		if known[rel] != store.HashOf(content) {
			pending++
		}
	}

	state := "сервер недоступен"
	res, err := d.hc.Get(d.server + "/healthz")
	if err == nil {
		defer res.Body.Close()
		var h struct {
			Seq int64 `json:"seq"`
		}
		if json.NewDecoder(res.Body).Decode(&h) == nil {
			state = fmt.Sprintf("сервер на seq %d", h.Seq)
		}
	}

	fmt.Printf("свод %s\n%d файлов, %d ждут заливки\n%s\n", d.vault, len(files), pending, state)
}

func (d *daemon) request(ctx context.Context, method, path string, body *bytes.Reader) (*http.Request, error) {
	var req *http.Request
	var err error
	if body == nil {
		req, err = http.NewRequestWithContext(ctx, method, d.server+path, nil)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, d.server+path, body)
	}
	if err != nil {
		return nil, err
	}
	if d.token != "" {
		req.Header.Set("Authorization", "Bearer "+d.token)
	}
	req.Header.Set(proto.HeaderDevice, d.device)
	req.Header.Set("Content-Type", "text/markdown; charset=utf-8")
	return req, nil
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

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "неизвестное устройство"
	}
	return h
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
