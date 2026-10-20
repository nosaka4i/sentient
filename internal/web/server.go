// Package web serves sentient's web UI (`sen serve`): server-rendered
// pages from the embedded templates in /web, with data from sentientd over
// the local API. It runs as the user, never inside sentientd.
package web

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/nosaka4i/sentient/internal/api"
	"github.com/nosaka4i/sentient/internal/health"
	"github.com/nosaka4i/sentient/internal/query"
	"github.com/nosaka4i/sentient/internal/render"
	"github.com/nosaka4i/sentient/internal/version"
	assets "github.com/nosaka4i/sentient/web"
)

// RefreshSeconds is how often pages reload themselves (until live updates
// over SSE land).
const RefreshSeconds = 30

// Source is what the pages need from sentientd (api.Client).
type Source interface {
	Status(ctx context.Context, from, to time.Time) (*query.Status, string, error)
	Health(ctx context.Context) (*health.Report, error)
	PromptDetail(ctx context.Context, id int64) (*query.PromptDetail, error)
}

// Server is the web UI.
type Server struct {
	src    Source
	socket string // shown when sentientd is unreachable
	now    func() time.Time
	home   string // the user's home, to show ~/… paths
	log    *slog.Logger
	pages  map[string]*template.Template
	static http.Handler
}

// New returns a Server reading from src. socket is only used in messages.
func New(src Source, socket string, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	home, _ := os.UserHomeDir()
	s := &Server{src: src, socket: socket, now: time.Now, home: home, log: log, pages: map[string]*template.Template{}}
	for _, page := range []string{"dashboard", "health", "prompt", "down"} {
		t, err := template.ParseFS(assets.FS, "templates/layout.html", "templates/"+page+".html")
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", page, err)
		}
		s.pages[page] = t
	}
	static, err := fs.Sub(assets.FS, "static")
	if err != nil {
		return nil, err
	}
	s.static = http.StripPrefix("/static/", http.FileServer(http.FS(static)))
	return s, nil
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.dashboard)
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /prompt/{id}", s.prompt)
	mux.Handle("GET /static/", s.static)
	return localOnly(mux)
}

// localOnly rejects requests whose Host isn't a loopback name, so a web
// page on another site can't read this one through DNS rebinding.
func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.Trim(host, "[]")
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			http.Error(w, "sen serve only answers on localhost", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// page is the data every page's layout uses.
type page struct {
	Title   string
	Version string
	Refresh int
	Live    bool
	Since   *since
	Nav     string // current header link ("health")
	Sub     bool   // a sub-page (back link, tighter top spacing)
}

type since struct{ ISO, Date, Ago string }

type dashboardPage struct {
	page
	AttentionCount int
	Tiles          []tile
	PromptsTotal   int64
	Prompts        []promptView
}

type downPage struct {
	page
	Socket string
	Detail string
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	st, _, err := s.src.Status(r.Context(), now.Add(-24*time.Hour), now)
	if err != nil {
		s.down(w, err)
		return
	}
	p := dashboardPage{
		page:         page{Title: "status", Version: version.Version, Refresh: RefreshSeconds, Live: true},
		Tiles:        tiles(st.Activity),
		PromptsTotal: st.PromptsTotal,
	}
	if !st.HistoryBegins.IsZero() {
		p.Since = &since{
			ISO:  st.HistoryBegins.In(now.Location()).Format("2006-01-02T15:04"),
			Date: render.Date(st.HistoryBegins, now),
			Ago:  render.Ago(st.HistoryBegins, now),
		}
	}
	for _, pr := range st.Prompts {
		p.Prompts = append(p.Prompts, newPromptView(pr, now))
	}
	s.render(w, http.StatusOK, "dashboard", p)
}

// down renders the "sentientd is not reachable" page (503) for any error
// getting data; the detail is shown unless it's plain unreachability.
func (s *Server) down(w http.ResponseWriter, err error) {
	p := downPage{
		page:   page{Title: "sentientd unreachable", Version: version.Version, Refresh: RefreshSeconds},
		Socket: s.socket,
	}
	if !errors.Is(err, api.ErrUnreachable) {
		p.Detail = err.Error()
		s.log.Error("status from sentientd", "err", err)
	}
	s.render(w, http.StatusServiceUnavailable, "down", p)
}

func (s *Server) render(w http.ResponseWriter, code int, name string, data any) {
	var b strings.Builder
	if err := s.pages[name].ExecuteTemplate(&b, "layout", data); err != nil {
		s.log.Error("render page", "page", name, "err", err)
		http.Error(w, "internal error rendering page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(b.String()))
}

// ActivityHref links a tile to its activity list.
func ActivityHref(resource, action string) string {
	return "/activity?" + url.Values{"resource": {resource}, "action": {action}}.Encode()
}
