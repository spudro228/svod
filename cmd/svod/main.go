// Команда svod — демон и CLI на машине.
//
// Следит за папкой свода и держит её в согласии с сервером в обе стороны.
// Локальные файлы остаются источником правды: сервер лишь развозит их
// между машинами и хранит историю.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spudro228/svod/internal/local"
	"github.com/spudro228/svod/internal/store"
	"github.com/spudro228/svod/internal/watch"
)

// version подставляется при сборке релиза через -ldflags.
// В сборке из исходников остаётся «из исходников».
var version = "из исходников"

func main() {
	var (
		vault  = flag.String("vault", env("SVOD_VAULT", "."), "папка свода")
		server = flag.String("server", env("SVOD_SERVER", "http://localhost:8080"), "адрес сервера")
		token  = flag.String("token", os.Getenv("SVOD_TOKEN"), "токен доступа")
		device = flag.String("device", env("SVOD_DEVICE", hostname()), "имя устройства для истории версий")
		once   = flag.Bool("once", false, "синхронизировать один раз и выйти")
		clone  = flag.Bool("clone", false, "скачать весь свод в пустую папку и выйти")
		force  = flag.Bool("force", false, "разрешить clone в непустую папку")
		status = flag.Bool("status", false, "показать состояние и выйти")
		exts   = flag.String("ext", local.DefaultExts,
			"ограничить расширениями через запятую; по умолчанию синхронизируется всё")
	)
	showVersion := flag.Bool("version", false, "показать версию и выйти")
	flag.Parse()

	if *showVersion {
		fmt.Println("svod " + version)
		return
	}

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

	agent := &local.Agent{
		Vault:  root,
		Server: strings.TrimSuffix(*server, "/"),
		Token:  *token,
		Device: *device,
		State:  st,
		Ign:    watch.NewIgnore(root, extList),
		HC:     &http.Client{Timeout: 60 * time.Second},
		Log:    log,
	}

	if *status {
		printStatus(agent)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("свод", "vault", root, "server", agent.Server, "device", agent.Device)

	if *clone {
		if err := runClone(ctx, agent, *force); err != nil {
			log.Error("скачать не удалось", "err", err)
			os.Exit(1)
		}
		return
	}

	// Порядок важен: сначала забираем чужое, потом отдаём своё.
	// Иначе локальная правка поверх устаревшей версии даст ложный конфликт.
	if err := agent.Pull(ctx); err != nil {
		log.Error("не смог забрать изменения", "err", err)
		if *once {
			os.Exit(1)
		}
	}
	if err := agent.SyncAll(ctx); err != nil {
		log.Error("первичная синхронизация не удалась", "err", err)
		if *once {
			os.Exit(1)
		}
	}
	if *once {
		report(agent, log)
		return
	}

	// Изменения с сервера: по событию и раз в полминуты на всякий случай.
	pull := func() {
		if err := agent.Pull(ctx); err != nil && ctx.Err() == nil {
			log.Warn("не смог забрать изменения", "err", err)
		}
	}
	go agent.Stream(ctx, pull)
	go func() {
		t := time.NewTicker(local.PollInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pull()
			}
		}
	}()

	w := watch.NewWatcher(root, agent.Ign, log)
	log.Info("слежу за папкой; Ctrl+C чтобы остановить")

	rescan := func() {
		// Каталог переименовали или перенесли: событий на файлы внутри
		// не будет, узнать о них можно только обходом.
		log.Info("менялся каталог, обхожу свод заново")
		if err := agent.SyncAll(ctx); err != nil && ctx.Err() == nil {
			log.Warn("обход не удался", "err", err)
		}
	}

	if err := w.Run(ctx, func(rel string) {
		if err := agent.SyncOne(ctx, rel); err != nil && ctx.Err() == nil {
			log.Warn("не смог синхронизировать", "path", rel, "err", err)
		}
	}, rescan); err != nil {
		log.Error("слежение оборвалось", "err", err)
	}
	report(agent, log)
}

// runClone скачивает свод целиком, показывая, сколько осталось.
func runClone(ctx context.Context, a *local.Agent, force bool) error {
	start := time.Now()
	var last time.Time

	err := a.Clone(ctx, force, func(done, total int) {
		// Печатаем не чаще двух раз в секунду и обязательно последнюю строку,
		// иначе быстрый свод зальёт терминал сотнями строк.
		if done < total && time.Since(last) < 500*time.Millisecond {
			return
		}
		last = time.Now()
		fmt.Printf("\rскачано %d из %d", done, total)
		if done == total {
			fmt.Println()
		}
	})
	if err != nil {
		fmt.Println()
		return err
	}

	fmt.Printf("свод скачан в %s за %s\n", a.Vault, time.Since(start).Round(time.Millisecond))
	fmt.Println("дальше запускай без -clone, чтобы держать папку в согласии с сервером")
	return nil
}

func report(a *local.Agent, log *slog.Logger) {
	if n := a.Conflicts(); n > 0 {
		log.Warn("конфликтов развёрнуто", "сколько", n,
			"что делать", "открой файлы со словом «конфликт» в имени и сведи руками")
	}
}

func printStatus(a *local.Agent) {
	files, err := watch.Scan(a.Vault, a.Ign)
	if err != nil {
		fmt.Printf("свод %s — не смог обойти: %v\n", a.Vault, err)
		return
	}
	known, _ := a.State.All()

	var pending, conflicts int
	for _, rel := range files {
		if strings.Contains(rel, "(конфликт,") {
			conflicts++
		}
		content, err := os.ReadFile(filepath.Join(a.Vault, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		if known[rel] != store.HashOf(content) {
			pending++
		}
	}

	lastSeq, _ := a.State.LastSeq()
	state := "сервер недоступен"
	if res, err := a.HC.Get(a.Server + "/healthz"); err == nil {
		defer res.Body.Close()
		var h struct {
			Seq int64 `json:"seq"`
		}
		if json.NewDecoder(res.Body).Decode(&h) == nil {
			switch {
			case h.Seq == lastSeq:
				state = fmt.Sprintf("в согласии с сервером, seq %d", h.Seq)
			default:
				state = fmt.Sprintf("сервер на seq %d, мы дочитали до %d", h.Seq, lastSeq)
			}
		}
	}

	fmt.Printf("свод %s\n%d файлов, %d ждут заливки\n%s\n", a.Vault, len(files), pending, state)
	if conflicts > 0 {
		fmt.Printf("%d конфликтных копий — сведи их руками\n", conflicts)
	}
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
