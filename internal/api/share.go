package api

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/spudro228/svod/internal/proto"
	"github.com/spudro228/svod/internal/store"
)

// Временные ссылки: показать одну заметку тому, у кого нет токена.
//
// Это единственная часть сервера, отвечающая без ключа, поэтому здесь
// всё построено вокруг одного правила: ссылка открывает ровно тот путь,
// который записан в её строке, и ровно те вложения, что были встроены
// в заметку в момент выдачи. Ни дерева, ни поиска, ни истории,
// ни соседних файлов.

const (
	shareTTLDefault = 24 * time.Hour
	shareTTLMax     = 30 * 24 * time.Hour
)

// createShare выдаёт ссылку. Требует токена — это действие владельца.
func (s *Server) createShare(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path  string `json:"path"`
		Hours int    `json:"hours"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "не разобрал запрос")
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		writeErr(w, http.StatusBadRequest, "не указан путь")
		return
	}

	ttl := shareTTLDefault
	if req.Hours > 0 {
		ttl = time.Duration(req.Hours) * time.Hour
	}
	if ttl > shareTTLMax {
		ttl = shareTTLMax
	}

	sh, err := s.st.CreateShare(req.Path, ttl)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "нет такой заметки")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	sh.URL = shareURL(r, sh.Key)
	s.log.Info("выдана временная ссылка", "path", sh.Path, "до", time.Unix(sh.Expires, 0))
	writeJSON(w, http.StatusOK, sh)
}

func (s *Server) listShares(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.Shares()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for i := range list {
		list[i].URL = shareURL(r, list[i].Key)
	}
	writeJSON(w, http.StatusOK, map[string]any{"shares": list})
}

func (s *Server) revokeShare(w http.ResponseWriter, r *http.Request) {
	err := s.st.RevokeShare(chi.URLParam(r, "key"))
	if errors.Is(err, store.ErrShareGone) {
		writeErr(w, http.StatusNotFound, "нет такой ссылки")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ───────────────────── то, что видит гость ─────────────────────

// sharedNote отдаёт единственную заметку, на которую выдана ссылка.
//
// Обрати внимание: путь берётся из строки ссылки, а не из запроса.
// Гость физически не может попросить другую заметку — параметра для этого
// в ручке нет.
func (s *Server) sharedNote(w http.ResponseWriter, r *http.Request) {
	sh, _, err := s.st.Share(chi.URLParam(r, "key"))
	if err != nil {
		s.shareGone(w)
		return
	}

	note, err := s.st.Note(sh.Path)
	if err != nil {
		s.shareGone(w)
		return
	}
	// Бэклинки и исходящие ссылки гостю не показываем: они выдают
	// названия соседних заметок, которых он видеть не должен.
	note.Backlinks = nil
	note.Links = nil

	noIndex(w)
	writeJSON(w, http.StatusOK, map[string]any{
		"note":    note,
		"expires": sh.Expires,
	})
}

// sharedAsset отдаёт вложение, но только из списка, зафиксированного
// при выдаче ссылки. Здесь легче всего было бы случайно открыть гостю
// всё хранилище блобов, поэтому проверка явная.
func (s *Server) sharedAsset(w http.ResponseWriter, r *http.Request) {
	sh, allowed, err := s.st.Share(chi.URLParam(r, "key"))
	if err != nil {
		s.shareGone(w)
		return
	}

	name := pathParam(r)
	target := s.resolveShared(sh.Path, name)
	if target == "" {
		s.shareGone(w)
		return
	}

	hash, err := s.st.Hash(target)
	if err != nil || hash == "" {
		s.shareGone(w)
		return
	}

	if !contains(allowed, hash) {
		// Файл существует, но в этой заметке его нет. Отвечаем так же,
		// как на несуществующий, чтобы по разнице ответов нельзя было
		// проверять наличие файлов в своде.
		s.shareGone(w)
		return
	}

	b, err := s.st.Blob(hash)
	if err != nil {
		s.shareGone(w)
		return
	}

	ct := mime.TypeByExtension(strings.ToLower(path.Ext(target)))
	if ct == "" {
		ct = "application/octet-stream"
	}
	noIndex(w)
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Write(b)
}

// resolveShared повторяет разрешение вложений страницей: сперва полный
// путь, затем имя файла.
func (s *Server) resolveShared(notePath, target string) string {
	if h, _ := s.st.Hash(target); h != "" {
		return target
	}
	tree, err := s.st.Tree()
	if err != nil {
		return ""
	}
	base := strings.ToLower(path.Base(target))
	for _, f := range tree.Files {
		if strings.ToLower(path.Base(f.Path)) == base {
			return f.Path
		}
	}
	return ""
}

// shareGone — единственный ответ на всё, что пошло не так: нет ссылки,
// истекла, отозвана, файла не существует, файл не из этой заметки.
// Различать эти случаи снаружи нельзя.
func (s *Server) shareGone(w http.ResponseWriter) {
	noIndex(w)
	writeErr(w, http.StatusNotFound, "ссылка недействительна или истекла")
}

func noIndex(w http.ResponseWriter) {
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

func shareURL(r *http.Request, key string) string {
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/s/" + key
}

func contains(list []string, v string) bool {
	for _, it := range list {
		if it == v {
			return true
		}
	}
	return false
}

var _ = proto.Share{}
