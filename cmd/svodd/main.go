// Команда svodd — сервер свода.
//
// Хранит канонический слепок, присваивает изменениям порядок, отдаёт API
// и веб-клиент. Веб-клиент вшит в бинарник, поэтому деплой — это скопировать
// один файл.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spudro228/svod/internal/api"
	"github.com/spudro228/svod/internal/store"
	"github.com/spudro228/svod/internal/webui"
)

// version подставляется при сборке релиза через -ldflags.
// В сборке из исходников остаётся «из исходников».
var version = "из исходников"

func main() {
	var (
		addr    = flag.String("addr", env("SVOD_ADDR", ":8080"), "адрес прослушивания")
		data    = flag.String("data", env("SVOD_DATA", "./data"), "каталог с meta.db и blobs/")
		token   = flag.String("token", os.Getenv("SVOD_TOKEN"), "токен доступа; пустой отключает проверку")
		origins = flag.String("origins", os.Getenv("SVOD_ORIGINS"),
			"через запятую: откуда ещё разрешён WebSocket, например localhost:5173 для дев-сервера")
	)
	showVersion := flag.Bool("version", false, "показать версию и выйти")
	flag.Parse()

	if *showVersion {
		fmt.Println("svod " + version)
		return
	}

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	st, err := store.Open(*data)
	if err != nil {
		log.Error("не смог открыть хранилище", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	seq, _ := st.Seq()
	log.Info("хранилище открыто", "data", *data, "seq", seq, "fts5", st.HasFTS())
	if *token == "" {
		log.Warn("токен не задан — доступ открыт всем; так можно только локально")
	}

	var originList []string
	for _, o := range strings.Split(*origins, ",") {
		if o = strings.TrimSpace(o); o != "" {
			originList = append(originList, o)
		}
	}
	if len(originList) > 0 {
		log.Info("WebSocket разрешён и с других адресов", "origins", originList)
	}

	handler := api.New(st, *token, originList, webAssets(log), log)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// WebSocket живёт долго, поэтому общий WriteTimeout не ставим.
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Info("сервер слушает", "addr", *addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("сервер упал", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("останавливаюсь")

	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
}

// webAssets отдаёт вшитый веб-клиент. Если фронт не собран, сервер поднимется
// без него: API останется рабочим.
func webAssets(log *slog.Logger) fs.FS {
	sub, err := fs.Sub(webui.Dist, "dist")
	if err != nil {
		log.Warn("веб-клиент не вшит", "err", err)
		return nil
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		log.Warn("веб-клиент не собран — работает только API; собери его через make web")
		return nil
	}
	return sub
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
