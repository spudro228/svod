// Package api — HTTP-слой сервера на chi.
//
// chi выбран за то, что хендлер остаётся обычным http.Handler: WebSocket
// и любой сторонний middleware работают без переходников.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/spudro228/svod/internal/proto"
	"github.com/spudro228/svod/internal/store"
)

// Заметка столько не весит, а картинка вполне может.
const maxFileSize = 32 << 20

type Server struct {
	st    *store.Store
	token string
	hub   *hub
	log   *slog.Logger
}

// New собирает роутер. Пустой token отключает проверку доступа —
// это режим локального демо, на VPS так делать нельзя.
func New(st *store.Store, token string, webFS fs.FS, log *slog.Logger) http.Handler {
	s := &Server{st: st, token: token, hub: newHub(), log: log}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))
	r.Use(s.accessLog)

	r.Get("/healthz", s.health)

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(s.auth)

		r.Get("/tree", s.tree)
		r.Get("/changes", s.changes)
		r.Get("/search", s.search)
		r.Get("/blob/{hash}", s.blob)
		r.Get("/note/*", s.note)
		r.Get("/history/*", s.history)
		r.Get("/raw/*", s.raw)
		r.Get("/tags", s.tags)
		r.Put("/files/*", s.put)
		r.Delete("/files/*", s.del)
		r.Get("/stream", s.stream)
	})

	if webFS != nil {
		r.NotFound(spaHandler(webFS))
	}
	return r
}

// Notify рассылает подписчикам новый seq.
func (s *Server) Notify(ev proto.StreamEvent) { s.hub.broadcast(ev) }

// ───────────────────────── middleware ─────────────────────────

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.token == "" {
			next.ServeHTTP(w, r)
			return
		}
		if bearer(r) == s.token {
			next.ServeHTTP(w, r)
			return
		}
		if c, err := r.Cookie("svod_session"); err == nil && c.Value == s.token {
			next.ServeHTTP(w, r)
			return
		}
		writeErr(w, http.StatusUnauthorized, "нужен токен доступа")
	})
}

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			s.log.Info("запрос",
				"method", r.Method, "path", r.URL.Path,
				"status", ww.Status(), "ms", time.Since(start).Milliseconds())
		}
	})
}

// ───────────────────────── хендлеры ─────────────────────────

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	seq, err := s.st.Seq()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "seq": seq, "fts": s.st.HasFTS(),
	})
}

func (s *Server) tree(w http.ResponseWriter, r *http.Request) {
	t, err := s.st.Tree()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) changes(w http.ResponseWriter, r *http.Request) {
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	c, err := s.st.Changes(since)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	hits, err := s.st.Search(r.URL.Query().Get("q"), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hits": hits})
}

func (s *Server) blob(w http.ResponseWriter, r *http.Request) {
	b, err := s.st.Blob(chi.URLParam(r, "hash"))
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "блоб не найден")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Содержимое неизменяемо — можно кешировать навсегда.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(b)
}

func (s *Server) note(w http.ResponseWriter, r *http.Request) {
	p := pathParam(r)
	n, err := s.st.Note(p)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "заметка не найдена: "+p)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	versions, err := s.st.History(pathParam(r), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": versions})
}

// raw отдаёт файл по пути с правильным типом содержимого.
// Через эту ручку страница показывает картинки и вложения.
func (s *Server) raw(w http.ResponseWriter, r *http.Request) {
	p := pathParam(r)
	hash, err := s.st.Hash(p)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if hash == "" {
		writeErr(w, http.StatusNotFound, "файл не найден: "+p)
		return
	}
	b, err := s.st.Blob(hash)
	if err != nil {
		writeErr(w, http.StatusNotFound, "содержимое потеряно")
		return
	}

	ct := mime.TypeByExtension(strings.ToLower(path.Ext(p)))
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	// Хеш в ETag: содержимое неизменяемо, поэтому кеш безопасен.
	w.Header().Set("ETag", `"`+hash+`"`)
	w.Header().Set("Cache-Control", "private, max-age=60")
	if match := r.Header.Get("If-None-Match"); match == `"`+hash+`"` {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Write(b)
}

func (s *Server) tags(w http.ResponseWriter, r *http.Request) {
	if tag := r.URL.Query().Get("tag"); tag != "" {
		paths, err := s.st.ByTag(tag)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"paths": paths})
		return
	}
	all, err := s.st.Tags()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": all})
}

func (s *Server) put(w http.ResponseWriter, r *http.Request) {
	p := pathParam(r)
	if p == "" {
		writeErr(w, http.StatusBadRequest, "пустой путь")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxFileSize+1))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(body) > maxFileSize {
		writeErr(w, http.StatusRequestEntityTooLarge, "файл больше 32 МБ")
		return
	}

	res, err := s.st.Put(p, body, baseHash(r), device(r))
	if errors.Is(err, store.ErrConflict) {
		s.conflict(w, p)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.hub.broadcast(proto.StreamEvent{Seq: res.Seq, Path: p})
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) del(w http.ResponseWriter, r *http.Request) {
	p := pathParam(r)
	res, err := s.st.Delete(p, baseHash(r), device(r))
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "нечего удалять: "+p)
	case errors.Is(err, store.ErrConflict):
		s.conflict(w, p)
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
	default:
		s.hub.broadcast(proto.StreamEvent{Seq: res.Seq, Path: p})
		writeJSON(w, http.StatusOK, res)
	}
}

func (s *Server) conflict(w http.ResponseWriter, p string) {
	cur, _ := s.st.Hash(p)
	seq, _ := s.st.Seq()
	writeJSON(w, http.StatusConflict, proto.Conflict{
		Error: "на сервере другая версия", ServerHash: cur, Seq: seq,
	})
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // локальное демо; на VPS ограничить Origin
	})
	if err != nil {
		return
	}
	defer c.CloseNow()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	ch := s.hub.subscribe()
	defer s.hub.unsubscribe(ch)

	// Читаем в фон, чтобы поймать закрытие соединения клиентом.
	go func() {
		for {
			if _, _, err := c.Read(ctx); err != nil {
				cancel()
				return
			}
		}
	}()

	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-ch:
			wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
			err := wsjson.Write(wctx, c, ev)
			wcancel()
			if err != nil {
				return
			}
		case <-ping.C:
			pctx, pcancel := context.WithTimeout(ctx, 5*time.Second)
			err := c.Ping(pctx)
			pcancel()
			if err != nil {
				return
			}
		}
	}
}

// ───────────────────────── статика ─────────────────────────

// spaHandler отдаёт собранный веб-клиент, а на неизвестные пути — index.html,
// чтобы работала навигация внутри приложения.
func spaHandler(webFS fs.FS) http.HandlerFunc {
	files := http.FileServer(http.FS(webFS))
	return func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(webFS, p); err != nil {
			index, err := fs.ReadFile(webFS, "index.html")
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(index)
			return
		}
		files.ServeHTTP(w, r)
	}
}

// ───────────────────────── шина событий ─────────────────────────

type hub struct {
	mu   sync.Mutex
	subs map[chan proto.StreamEvent]struct{}
}

func newHub() *hub {
	return &hub{subs: make(map[chan proto.StreamEvent]struct{})}
}

func (h *hub) subscribe() chan proto.StreamEvent {
	ch := make(chan proto.StreamEvent, 32)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *hub) unsubscribe(ch chan proto.StreamEvent) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

func (h *hub) broadcast(ev proto.StreamEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default: // подписчик не успевает — пропускаем, он догонит через /changes
		}
	}
}

// ───────────────────────── мелочи ─────────────────────────

// pathParam достаёт путь из wildcard-маршрута.
//
// chi маршрутизирует по RawPath, если тот отличается от Path, поэтому для
// путей с кириллицей и эмодзи параметр приезжает в процентной кодировке.
func pathParam(r *http.Request) string {
	raw := chi.URLParam(r, "*")
	if dec, err := url.PathUnescape(raw); err == nil {
		return dec
	}
	return raw
}

// device читает имя машины, приславшей изменение.
func device(r *http.Request) string {
	return proto.DecodeDevice(r.Header.Get(proto.HeaderDevice))
}

// baseHash читает If-Match. Отсутствие заголовка означает «файла ещё нет».
func baseHash(r *http.Request) string {
	return strings.Trim(r.Header.Get(proto.HeaderIfMatch), `"`)
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if after, ok := strings.CutPrefix(h, "Bearer "); ok {
		return after
	}
	return ""
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
