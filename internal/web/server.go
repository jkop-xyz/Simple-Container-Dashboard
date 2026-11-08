// Package web serves the dashboard: server-rendered pages plus a small embedded JS file.
package web

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/config"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/dockerapi"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/engine"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/stacks"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/state"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/view"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

var pages = template.Must(template.New("").Funcs(template.FuncMap{
	"when": func(t time.Time) string { return t.Local().Format("2006-01-02 15:04:05") },
	"ago": func(t time.Time) string {
		d := time.Since(t).Round(time.Minute)
		switch {
		case d < time.Minute:
			return "just now"
		case d < time.Hour:
			return fmt.Sprintf("%dm ago", int(d.Minutes()))
		case d < 48*time.Hour:
			return fmt.Sprintf("%dh ago", int(d.Hours()))
		}
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	},
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
	Latest(ctx context.Context) (string, error)
	Files(ctx context.Context, rev string) ([]string, error)
}

// Deps are the server's dependencies.
type Deps struct {
	Runtime Runtime
	// RuntimeKind is the host's runtime (docker or podman).
	RuntimeKind config.Runtime
	// Repo is nil when no Deployment Repo is configured.
	Repo Repo
	// RepoName is the Deployment Repo's display name, e.g. "owner/name".
	RepoName string
	// RepoError reports why the clone is unavailable (e.g. the initial clone failed).
	RepoError func() error
	Hostname  string
	State     *state.Store
	// Engine runs Sync. Nil when no Deployment Repo is configured.
	Engine *engine.Engine
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
	s.mux.HandleFunc("POST /sync", s.sync)
	s.mux.HandleFunc("POST /apply", s.apply)
	s.mux.HandleFunc("GET /apply/stream", s.applyStream)
	s.mux.HandleFunc("GET /history", s.history)
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
	// Sync is nil when no Deployment Repo is configured.
	Sync *engine.Status
	// LastApply is the most recent finished Apply, if any.
	LastApply *state.ApplyRecord
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
	if len(st.History) > 0 {
		data.LastApply = &st.History[0]
	}
	if s.deps.Engine != nil {
		status := s.deps.Engine.Status()
		data.Sync = &status
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := pages.ExecuteTemplate(w, "index.html", data); err != nil {
		slog.Error("rendering index", "err", err)
	}
}

// discover returns the Stacks in the clone's checked-out commit plus any new ones in the
// latest fetched commit, so Stacks that only exist upstream can be selected before an
// Apply brings them in.
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
	revs := []string{head}
	if latest, err := s.deps.Repo.Latest(ctx); err == nil && latest != head {
		revs = append(revs, latest)
	}
	var all []string
	for _, rev := range revs {
		files, err := s.deps.Repo.Files(ctx, rev)
		if err != nil {
			return nil, head, err
		}
		all = append(all, files...)
	}
	return stacks.Discover(all, s.deps.RuntimeKind), head, nil
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

// sync runs (or joins) a Sync, then shows the dashboard with the resulting Sync Plan or
// the reason it was refused.
func (s *Server) sync(w http.ResponseWriter, r *http.Request) {
	if s.deps.Engine == nil {
		http.Error(w, "no Deployment Repo configured", http.StatusNotFound)
		return
	}
	// The outcome is kept by the engine and rendered on the dashboard.
	_, _ = s.deps.Engine.Sync(r.Context())
	http.Redirect(w, r, "/#sync", http.StatusSeeOther)
}

// apply starts (or joins) an Apply of the current Sync Plan. Rejections are kept by the
// engine and shown on the dashboard.
func (s *Server) apply(w http.ResponseWriter, r *http.Request) {
	if s.deps.Engine == nil {
		http.Error(w, "no Deployment Repo configured", http.StatusNotFound)
		return
	}
	if _, err := s.deps.Engine.Apply(); err != nil {
		slog.Warn("Apply rejected", "err", err)
	}
	http.Redirect(w, r, "/#apply", http.StatusSeeOther)
}

// applyStream streams the running (or latest) Apply's output as Server-Sent Events,
// starting after the first `from` lines, and ends with a "done" event.
func (s *Server) applyStream(w http.ResponseWriter, r *http.Request) {
	if s.deps.Engine == nil {
		http.Error(w, "no Deployment Repo configured", http.StatusNotFound)
		return
	}
	run := s.deps.Engine.LastApply()
	if run == nil {
		http.Error(w, "no Apply has run", http.StatusNotFound)
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("from"))
	sse(w, r, func(send func(event, data string) error) error {
		for {
			lines, done, err := run.Output.Next(r.Context(), offset)
			if err != nil {
				return err
			}
			if done {
				return send("done", "")
			}
			for _, l := range lines {
				if err := send("", l); err != nil {
					return err
				}
			}
			offset += len(lines)
		}
	})
}

// history shows the most recent Applies.
func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	st, err := s.deps.State.Get()
	if err != nil {
		http.Error(w, "reading state: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := struct {
		Hostname string
		History  []state.ApplyRecord
	}{s.deps.Hostname, st.History}
	if err := pages.ExecuteTemplate(w, "history.html", data); err != nil {
		slog.Error("rendering history", "err", err)
	}
}
