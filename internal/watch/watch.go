// Package watch следит за папкой свода: первичный обход, fsnotify,
// дебаунс и правила игнорирования.
package watch

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Debounce — сколько тишины ждём после последнего события по пути.
// Редакторы пишут файл в три-четыре приёма, без паузы на сервер уедут
// три версии вместо одной.
const Debounce = 500 * time.Millisecond

// DefaultIgnores — то, что не синхронизируется никогда.
var DefaultIgnores = []string{
	".svod/", ".git/", ".obsidian/workspace.json", ".obsidian/cache",
	".DS_Store", "*.swp", "*.swx", "*~", ".Trash/", "node_modules/",
	".svod-*.tmp", // временные файлы собственной атомарной записи
}

// Ignore решает, попадает ли путь в свод.
type Ignore struct {
	patterns []string
	exts     map[string]bool
}

// NewIgnore собирает правила: встроенные плюс .svodignore из корня свода.
// exts — расширения, которые синхронизируем (пустой набор означает «все»).
func NewIgnore(root string, exts []string) *Ignore {
	ig := &Ignore{patterns: append([]string{}, DefaultIgnores...), exts: map[string]bool{}}
	for _, e := range exts {
		ig.exts[strings.ToLower(e)] = true
	}
	if b, err := os.ReadFile(filepath.Join(root, ".svodignore")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				ig.patterns = append(ig.patterns, line)
			}
		}
	}
	return ig
}

// Match сообщает, нужно ли пропустить файл.
func (ig *Ignore) Match(rel string) bool {
	if ig.matchPatterns(rel) {
		return true
	}
	// Фильтр по расширению — только для файлов: у каталогов расширения нет,
	// и применять его к ним значит отрезать всё дерево.
	if len(ig.exts) > 0 && !ig.exts[strings.ToLower(filepath.Ext(rel))] {
		return true
	}
	return false
}

// MatchDir сообщает, нужно ли пропустить каталог целиком.
func (ig *Ignore) MatchDir(rel string) bool {
	return ig.matchPatterns(rel)
}

func (ig *Ignore) matchPatterns(rel string) bool {
	rel = strings.TrimSuffix(filepath.ToSlash(rel), "/")
	base := filepath.Base(rel)

	for _, p := range ig.patterns {
		switch {
		case strings.HasSuffix(p, "/"):
			dir := strings.TrimSuffix(p, "/")
			if rel == dir || strings.HasPrefix(rel, dir+"/") || strings.Contains(rel, "/"+dir+"/") {
				return true
			}
		case strings.ContainsAny(p, "*?["):
			if ok, _ := filepath.Match(p, base); ok {
				return true
			}
		default:
			if rel == p || base == p || strings.HasPrefix(rel, p+"/") {
				return true
			}
		}
	}
	return false
}

// Scan обходит свод и возвращает относительные пути всех подходящих файлов.
func Scan(root string, ig *Ignore) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // нет доступа к ветке — пропускаем, а не падаем
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil || rel == "." {
			return nil
		}
		if d.IsDir() {
			if ig.MatchDir(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if !ig.Match(rel) {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	return out, err
}

// Watcher следит за деревом и отдаёт пути пачками после дебаунса.
type Watcher struct {
	root string
	ig   *Ignore
	log  *slog.Logger

	mu      sync.Mutex
	pending map[string]*time.Timer
}

func NewWatcher(root string, ig *Ignore, log *slog.Logger) *Watcher {
	return &Watcher{root: root, ig: ig, log: log, pending: map[string]*time.Timer{}}
}

// Run блокируется до отмены контекста, вызывая onChange для каждого пути,
// по которому наступила тишина.
func (w *Watcher) Run(ctx context.Context, onChange func(rel string)) error {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer fw.Close()

	if err := w.addTree(fw, w.root); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil

		case ev, ok := <-fw.Events:
			if !ok {
				return nil
			}
			rel, rerr := filepath.Rel(w.root, ev.Name)
			if rerr != nil {
				continue
			}
			rel = filepath.ToSlash(rel)

			// Новый каталог — начинаем следить и за ним.
			if ev.Op&fsnotify.Create != 0 {
				if st, err := os.Stat(ev.Name); err == nil && st.IsDir() {
					if !w.ig.MatchDir(rel) {
						_ = w.addTree(fw, ev.Name)
					}
					continue
				}
			}
			if w.ig.Match(rel) {
				continue
			}
			w.schedule(rel, onChange)

		case err, ok := <-fw.Errors:
			if !ok {
				return nil
			}
			w.log.Warn("fsnotify", "err", err)
		}
	}
}

// schedule перезапускает таймер дебаунса для пути.
func (w *Watcher) schedule(rel string, onChange func(string)) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if t, ok := w.pending[rel]; ok {
		t.Stop()
	}
	w.pending[rel] = time.AfterFunc(Debounce, func() {
		w.mu.Lock()
		delete(w.pending, rel)
		w.mu.Unlock()
		onChange(rel)
	})
}

func (w *Watcher) addTree(fw *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(w.root, p)
		if rerr == nil && rel != "." && w.ig.MatchDir(filepath.ToSlash(rel)) {
			return filepath.SkipDir
		}
		if err := fw.Add(p); err != nil {
			w.log.Warn("не смог следить за каталогом", "dir", p, "err", err)
		}
		return nil
	})
}
