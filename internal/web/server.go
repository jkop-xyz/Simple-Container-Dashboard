// Package web serves the dashboard: server-rendered pages plus a small embedded JS file.
package web

import (
	"context"
	"embed"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/dockerapi"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

var pages = template.Must(template.ParseFS(templateFS, "templates/*.html"))

// Runtime is what the web server needs from the container runtime.
type Runtime interface {
	ListContainers(ctx context.Context) ([]dockerapi.Container, error)
}

// Server is the dashboard HTTP handler.
type Server struct {
	runtime Runtime
	mux     *http.ServeMux
}

// New returns the dashboard handler.
func New(rt Runtime) *Server {
	s := &Server{runtime: rt, mux: http.NewServeMux()}
	static, _ := fs.Sub(staticFS, "static")
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	s.mux.HandleFunc("GET /{$}", s.index)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

type indexData struct {
	Containers []dockerapi.Container
	Error      string
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var data indexData
	status := http.StatusOK
	cs, err := s.runtime.ListContainers(ctx)
	if err != nil {
		slog.Error("listing containers", "err", err)
		data.Error = err.Error()
		status = http.StatusBadGateway
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].Name < cs[j].Name })
	data.Containers = cs

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := pages.ExecuteTemplate(w, "index.html", data); err != nil {
		slog.Error("rendering index", "err", err)
	}
}
