// Package web serves the dashboard: server-rendered pages plus a small embedded JS file.
package web

import (
	"context"
	"embed"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/dockerapi"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/stacks"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/state"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/view"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

var pages = template.Must(template.New("").Funcs(template.FuncMap{
	"short": func(commit string) string {
		if len(commit) > 7 {
			return commit[:7]
		}
		return commit
	},
}).ParseFS(templateFS, "templates/*.html"))

// Runtime is what the web server needs from the container runtime.
type Runtime interface {
	ListContainers(ctx context.Context) ([]dockerapi.Container, error)
}

// Repo is what the web server needs from the Deployment Repo clone.
type Repo interface {
	Head(ctx context.Context) (string, error)
	Files(ctx context.Context, rev string) ([]string, error)
}

// Deps are the server's dependencies.
type Deps struct {
	Runtime Runtime
	// Repo is nil when no Deployment Repo is configured.
	Repo Repo
	// RepoName is the Deployment Repo's display name, e.g. "owner/name".
	RepoName string
	// RepoError reports why the clone is unavailable (e.g. the initial clone failed).
	RepoError func() error
	Hostname  string
	State     *state.Store
}

// Server is the dashboard HTTP handler.
type Server struct {
	deps Deps
	mux  *http.ServeMux
}

// New returns the dashboard handler.
func New(deps Deps) *Server {
	s := &Server{deps: deps, mux: http.NewServeMux()}
	static, _ := fs.Sub(staticFS, "static")
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	s.mux.HandleFunc("GET /{$}", s.index)
	s.mux.HandleFunc("POST /stacks/{name}/selection", s.setSelection)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

type indexData struct {
	Hostname  string
	RepoName  string
	Commit    string
	RepoError string
	view.Dashboard
	Error string
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	data := indexData{Hostname: s.deps.Hostname, RepoName: s.deps.RepoName}
	status := http.StatusOK

	cs, err := s.deps.Runtime.ListContainers(ctx)
	if err != nil {
		slog.Error("listing containers", "err", err)
		data.Error = err.Error()
		status = http.StatusBadGateway
	}

	var ss []stacks.Stack
	if s.deps.Repo != nil {
		ss, data.Commit, err = s.discover(ctx)
		if err != nil {
			slog.Error("reading Deployment Repo", "err", err)
			data.RepoError = err.Error()
		}
	}
	st, err := s.deps.State.Get()
	if err != nil {
		slog.Error("reading state", "err", err)
		data.Error = err.Error()
		status = http.StatusInternalServerError
	}
	data.Dashboard = view.Build(cs, ss, st.IsSelected)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := pages.ExecuteTemplate(w, "index.html", data); err != nil {
		slog.Error("rendering index", "err", err)
	}
}

func (s *Server) discover(ctx context.Context) ([]stacks.Stack, string, error) {
	if s.deps.RepoError != nil {
		if err := s.deps.RepoError(); err != nil {
			return nil, "", err
		}
	}
	head, err := s.deps.Repo.Head(ctx)
	if err != nil {
		return nil, "", err
	}
	files, err := s.deps.Repo.Files(ctx, head)
	if err != nil {
		return nil, head, err
	}
	return stacks.Discover(files), head, nil
}

// setSelection selects or unselects a Stack. It changes only the selection: nothing is
// deployed or torn down until an Apply.
func (s *Server) setSelection(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	selected := r.FormValue("selected") == "true"
	if s.deps.Repo == nil {
		http.Error(w, "no Deployment Repo configured", http.StatusNotFound)
		return
	}
	ss, _, err := s.discover(r.Context())
	if err != nil {
		http.Error(w, "reading Deployment Repo: "+err.Error(), http.StatusBadGateway)
		return
	}
	if !slices.ContainsFunc(ss, func(st stacks.Stack) bool { return st.Name == name }) {
		http.Error(w, "no Stack named "+name, http.StatusNotFound)
		return
	}
	if err := s.deps.State.Update(func(st *state.State) { st.SetSelected(name, selected) }); err != nil {
		http.Error(w, "saving selection: "+err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("Stack selection changed", "stack", name, "selected", selected)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
