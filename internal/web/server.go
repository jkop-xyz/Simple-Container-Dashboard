// Package web serves the dashboard: server-rendered pages plus a small embedded JS file.
package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/config"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/crashloop"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/dockerapi"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/engine"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/stacks"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/state"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/systemd"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/usage"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/view"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

var pages = template.Must(template.New("").Funcs(template.FuncMap{
	"when": func(t time.Time) string { return t.Local().Format("2006-01-02 15:04:05") },
	"rel":  relTime,
	"took": took,
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
	"ports": view.Ports,
	"sicon": sicon,
	// groupState sums up a Stack's containers: the problems if there are any, otherwise
	// how many run. Never both, so it can't read as broken and fine at once.
	"groupState": func(g view.Group) string {
		var parts []string
		if n := g.CrashLooping(); n > 0 {
			parts = append(parts, fmt.Sprintf("%d crash-looping", n))
		}
		if n := g.Unhealthy(); n > 0 {
			parts = append(parts, fmt.Sprintf("%d unhealthy", n))
		}
		if len(parts) > 0 {
			return strings.Join(parts, " · ")
		}
		if len(g.Containers) == 0 {
			return "no containers yet"
		}
		if g.Running() < len(g.Containers) {
			return fmt.Sprintf("%d/%d running", g.Running(), len(g.Containers))
		}
		if g.Restarted() {
			return fmt.Sprintf("%d/%d running · restarted", g.Running(), len(g.Containers))
		}
		return fmt.Sprintf("%d/%d running", g.Running(), len(g.Containers))
	},
	// problems counts containers that need attention.
	"problems": func(cs []view.Container) int {
		n := 0
		for _, c := range cs {
			if c.NeedsAttention() {
				n++
			}
		}
		return n
	},
	// stackIcon is a Stack's status icon in the Overview summary.
	"stackIcon": func(g view.Group) string {
		switch {
		case g.Stack.Invalid() || g.Unhealthy() > 0 || g.CrashLooping() > 0:
			return "err"
		case len(g.Containers) == 0 || g.Running() < len(g.Containers) || g.Selected != g.Deployed:
			return "pend"
		}
		return "ok"
	},
	"filterTitle": func(f string) string { return view.FilterTitles[f] },
	// shown counts the containers a (filtered) dashboard shows.
	"shown": func(d view.Dashboard) int {
		n := len(d.Other)
		for _, g := range d.Groups {
			n += len(g.Containers)
		}
		return n
	},
	// memBar sizes a container's memory bar relative to the busiest container.
	// memBar sizes a memory bar on a square-root scale of the share of host memory, so
	// small containers stay visible (the exact share is in the tooltip).
	"memBar": func(mem, total uint64) float64 {
		if total == 0 || mem == 0 {
			return 0
		}
		return max(3, min(math.Sqrt(float64(mem)/float64(total))*100, 100))
	},
	"pct": func(part, total uint64) string {
		if total == 0 {
			return ""
		}
		return fmt.Sprintf("%.1f%% of host memory", float64(part)/float64(total)*100)
	},
	"navCount": navCount,
	"title": func(s string) string {
		if s == "" {
			return s
		}
		return strings.ToUpper(s[:1]) + s[1:]
	},
	"ifelse": func(cond any, a, b any) any {
		if truthy(cond) {
			return a
		}
		return b
	},
	"sub": func(a, b int) int { return a - b },
	// cpuBar is the CPU % clamped to 0–100, for the usage bar.
	// load classifies CPU % for the bar colour.
	"load": func(c view.Container) string {
		switch {
		case !c.HasCPU || c.State != "running":
			return ""
		case c.CPUPercent >= 85:
			return "bar-bad"
		case c.CPUPercent >= 50:
			return "bar-warn"
		}
		return ""
	},
	"cpuBar": func(c view.Container) float64 {
		if c.State != "running" || !c.HasCPU {
			return 0
		}
		return min(max(c.CPUPercent, 0), 100)
	},
	// line classifies Apply output lines so steps and failures stand out.
	"line": func(l string) string {
		switch {
		case strings.HasPrefix(l, "==>"):
			return "ln-step"
		case strings.HasPrefix(l, "!!!"):
			return "ln-error"
		}
		return ""
	},
	"dict": func(kv ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	},
	"health": healthText,
	"cpu":    cpuText,
	"mem":    memText,
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
	Inspect(ctx context.Context, id string) (dockerapi.Details, error)
	Stats(ctx context.Context, id string) (dockerapi.Sample, error)
	Logs(ctx context.Context, id string, opts dockerapi.LogOptions, fn func(line string) error) error
	Info(ctx context.Context) (dockerapi.Info, error)
}

// Units reads systemd units; only set on Podman hosts.
type Units interface {
	States(ctx context.Context, units []string) (map[string]systemd.UnitState, error)
	IsActive(ctx context.Context, unit string) string
	Journal(ctx context.Context, unit string, tail int, follow bool, out io.Writer) error
}

// AutoUpdateTimer is Podman's image auto-update timer, shown in the header.
const AutoUpdateTimer = "podman-auto-update.timer"

// LogTail is how many lines a logs view starts with; LogTails are the choices offered.
const LogTail = 500

// LogTails are the line counts a log view can start with.
var LogTails = []int{500, 2000, 10000}

// logTail reads the ?lines= choice, falling back to LogTail.
func logTail(r *http.Request) int {
	n, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	if slices.Contains(LogTails, n) {
		return n
	}
	return LogTail
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
	// Units is set on Podman hosts, for Quadlet unit states and logs.
	Units Units
	// Missing lists external tools the host lacks.
	Missing func() []string
	// NewerRelease returns a newer scd release tag, or "".
	NewerRelease func() string
	// Demo shows the demo-mode banner and its "simulate a push" button.
	Demo bool
}

// Server is the dashboard HTTP handler.
type Server struct {
	deps    Deps
	mux     *http.ServeMux
	usage   usage.Collector
	crashes crashloop.Tracker
	metrics metrics

	infoMu sync.Mutex
	info   dockerapi.Info
}

// hostInfo is the runtime's host info, read once it succeeds.
func (s *Server) hostInfo(ctx context.Context) dockerapi.Info {
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	if s.info.MemTotal == 0 {
		if info, err := s.deps.Runtime.Info(ctx); err == nil {
			s.info = info
		}
	}
	return s.info
}

// New returns the dashboard handler.
func New(deps Deps) *Server {
	s := &Server{deps: deps, mux: http.NewServeMux()}
	static, _ := fs.Sub(staticFS, "static")
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	s.mux.HandleFunc("GET /{$}", s.index)
	s.mux.HandleFunc("GET /containers", s.index)
	s.mux.HandleFunc("GET /deployment", s.index)
	s.mux.HandleFunc("POST /stacks/{name}/selection", s.setSelection)
	s.mux.HandleFunc("POST /sync", s.sync)
	s.mux.HandleFunc("POST /apply", s.apply)
	s.mux.HandleFunc("GET /apply/stream", s.applyStream)
	s.mux.HandleFunc("GET /history", s.history)
	s.mux.HandleFunc("GET /history/{id}", s.historyOutput)
	s.mux.HandleFunc("GET /api/status", s.status)
	s.mux.HandleFunc("GET /api/metrics", s.metricsJSON)
	s.mux.HandleFunc("GET /containers/{id}/logs", s.logsPage)
	s.mux.HandleFunc("GET /containers/{id}/logs/stream", s.logsStream)
	s.mux.HandleFunc("GET /units/{unit}/logs", s.unitLogsPage)
	s.mux.HandleFunc("GET /units/{unit}/logs/stream", s.unitLogsStream)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

type indexData struct {
	// BrowserHost is the hostname the browser used to reach the dashboard; port links
	// point at it.
	BrowserHost string
	Hostname    string
	RepoName    string
	Commit      string
	RepoError   string
	view.Dashboard
	Error string
	// Sync is nil when no Deployment Repo is configured.
	Sync *engine.Status
	// LastApply is the most recent finished Apply, if any.
	LastApply *state.ApplyRecord
	// AutoUpdate is the auto-update timer's state on Podman hosts ("" elsewhere).
	AutoUpdate string
	Missing    []string
	NewRelease string
	Demo       bool
	// Overview and Problems describe the whole host, even when a filter narrows the view.
	Overview view.Summary
	Problems []view.Problem
	// Filter is the active filter's name and FilterLabel what it shows ("" = everything).
	Filter, FilterLabel string
	// Sparks are the containers' CPU sparkline paths; MaxMem scales the memory bars.
	Sparks map[string]string
	MaxMem uint64
	// Counts are per-filter counts for the side navigation.
	Counts map[string]int
	// Q is the search text ("" = no search).
	Q string
	// View is the page: "overview", "containers" or "deployment".
	View string
	// MemTotal is the host's memory, which the memory bars are a share of.
	MemTotal uint64
	// Pending are Stacks whose selection differs from what's deployed.
	Pending []view.Group
	// ApplyHint explains a disabled Apply button.
	ApplyHint string
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	// Filters and searches live on the Containers page; keep old links working.
	if r.URL.Path == "/" && (r.URL.Query().Get("filter") != "" || r.URL.Query().Get("q") != "") {
		http.Redirect(w, r, "/containers?"+r.URL.RawQuery, http.StatusSeeOther)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	page := strings.TrimPrefix(r.URL.Path, "/")
	if page == "" {
		page = "overview"
	}
	data, status := s.dashboard(ctx, r, page)
	render(w, status, "index.html", data)
}

// dashboard gathers everything a dashboard page shows. Subpages use it too, so the app
// bar, toolbar and navigation counts look the same on every page.
func (s *Server) dashboard(ctx context.Context, r *http.Request, page string) (indexData, int) {
	data := indexData{Hostname: s.deps.Hostname, RepoName: s.deps.RepoName, BrowserHost: browserHost(r),
		Missing: s.missing(), NewRelease: s.newRelease(), Demo: s.deps.Demo, View: page}
	status := http.StatusOK

	cs, err := s.containers(ctx)
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
	full := view.Build(cs, ss, st.IsSelected)
	for i := range full.Groups {
		_, full.Groups[i].Deployed = st.Deployed[full.Groups[i].Stack.Name]
	}
	data.Overview, data.Problems = full.Summary(), full.Attention()
	data.Sparks = map[string]string{}
	for _, c := range cs {
		data.Sparks[c.ID] = sparkPath(s.metrics.spark(c.ID))
		data.MaxMem = max(data.MaxMem, c.Memory)
	}
	data.MemTotal = s.hostInfo(ctx).MemTotal
	if data.MemTotal > 0 {
		data.MaxMem = data.MemTotal // bars show each container's share of host memory
	}
	for _, g := range full.Groups {
		if g.Selected != g.Deployed && !g.Stack.Invalid() {
			data.Pending = append(data.Pending, g)
		}
	}
	data.Counts = map[string]int{
		"attention": len(data.Problems),
		"stopped":   data.Overview.Total - data.Overview.Running,
		"unmanaged": data.Overview.Unmanaged,
		"selected":  data.Overview.Selected,
	}
	data.Dashboard = full
	if f := r.URL.Query().Get("filter"); data.View == "containers" && view.Filters[f] != "" {
		data.Filter, data.FilterLabel = f, view.Filters[f]
		data.Dashboard = full.Filter(f)
	}
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" && data.View == "containers" {
		data.Q = q
		data.Dashboard = data.Dashboard.Search(q)
	}
	if s.deps.Units != nil {
		s.addUnits(ctx, &data.Dashboard)
		data.AutoUpdate = s.deps.Units.IsActive(ctx, AutoUpdateTimer)
	}
	if len(st.History) > 0 {
		data.LastApply = &st.History[0]
	}
	if s.deps.Engine != nil {
		status := s.deps.Engine.Status()
		data.Sync = &status
	}
	if data.Sync != nil && !data.Sync.CanApply {
		switch {
		case data.Sync.Running:
			data.ApplyHint = "A Sync is running"
		case data.Sync.Plan == nil:
			data.ApplyHint = "Nothing to apply yet: press Sync to fetch and review a plan"
		case data.Sync.Stale:
			data.ApplyHint = "The selection changed: press Sync again"
		case data.Sync.Plan.Blocked != "":
			data.ApplyHint = "This plan can't be applied"
		default:
			data.ApplyHint = "The plan changes nothing on this host"
		}
	}

	return data, status
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
	http.Redirect(w, r, backTo(r, "#sync"), http.StatusSeeOther)
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
	http.Redirect(w, r, backTo(r, "#apply"), http.StatusSeeOther)
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
	chrome, _ := s.dashboard(r.Context(), r, "history")
	data := struct {
		Hostname string
		History  []state.ApplyRecord
		Index    indexData
	}{s.deps.Hostname, st.History, chrome}
	render(w, http.StatusOK, "history.html", data)
}

// containers lists every container with its details and current usage. Inspect and
// stats run concurrently; a container whose details can't be read is still listed.
func (s *Server) containers(ctx context.Context) ([]view.Container, error) {
	cs, err := s.deps.Runtime.ListContainers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]view.Container, len(cs))
	keep := map[string]bool{}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, c := range cs {
		out[i].Container = c
		keep[c.ID] = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if d, err := s.deps.Runtime.Inspect(ctx, c.ID); err == nil {
				out[i].Details = d
				out[i].CrashLooping = s.crashes.Observe(c.ID, d.RestartCount)
			} else {
				slog.Debug("inspecting container", "id", c.ID, "err", err)
			}
			if c.State != "running" {
				return
			}
			if sample, err := s.deps.Runtime.Stats(ctx, c.ID); err == nil {
				out[i].Usage = s.usage.Update(c.ID, sample)
			} else {
				slog.Debug("container stats", "id", c.ID, "err", err)
			}
		}()
	}
	wg.Wait()
	s.usage.Forget(keep)
	s.crashes.Forget(keep)
	return out, nil
}

// statusContainer is one container in the JSON status, with display-ready values.
type statusContainer struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	State    string `json:"state"`
	Status   string `json:"status"`
	Health   string `json:"health"`
	Restarts int    `json:"restarts"`
	CPU      string `json:"cpu"`
	Memory   string `json:"memory"`
	// CrashLooping is set while the container keeps restarting.
	CrashLooping bool `json:"crashLooping"`
	// Spark is the CPU sparkline's SVG path; MemoryBytes sizes the memory bar.
	Spark       string `json:"spark"`
	MemoryBytes uint64 `json:"memoryBytes"`
}

// status is the JSON the dashboard polls to refresh container state and usage.
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	cs, err := s.containers(ctx)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	out := struct {
		Containers []statusContainer `json:"containers"`
		Missing    []string          `json:"missing"`
		NewRelease string            `json:"newRelease,omitempty"`
		MemTotal   uint64            `json:"memTotal"`
	}{Containers: []statusContainer{}, Missing: s.missing(), NewRelease: s.newRelease(), MemTotal: s.hostInfo(ctx).MemTotal}
	for _, c := range cs {
		out.Containers = append(out.Containers, statusContainer{
			ID: c.ID, Name: c.Name, State: c.State, Status: c.Status,
			Health: healthText(c.Health), Restarts: c.RestartCount,
			CPU: cpuText(c), Memory: memText(c), CrashLooping: c.CrashLooping,
			Spark: sparkPath(s.metrics.spark(c.ID)), MemoryBytes: c.Memory,
		})
	}
	json.NewEncoder(w).Encode(out)
}

func healthText(h string) string {
	if h == "" {
		return "—"
	}
	return h
}

func cpuText(c view.Container) string {
	if c.State != "running" || !c.HasCPU {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", c.CPUPercent)
}

func memText(c view.Container) string {
	if c.State != "running" || c.Memory == 0 {
		return "—"
	}
	const mib = 1 << 20
	if c.Memory >= 1<<30 {
		return fmt.Sprintf("%.1f GiB", float64(c.Memory)/(1<<30))
	}
	return fmt.Sprintf("%.0f MiB", float64(c.Memory)/mib)
}

// browserHost is the hostname (without port) the request was addressed to.
func browserHost(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.Host); err == nil {
		return host
	}
	return strings.Trim(r.Host, "[]")
}

// findContainer looks a container up by ID.
func (s *Server) findContainer(ctx context.Context, id string) (dockerapi.Container, bool, error) {
	cs, err := s.deps.Runtime.ListContainers(ctx)
	if err != nil {
		return dockerapi.Container{}, false, err
	}
	for _, c := range cs {
		if c.ID == id {
			return c, true, nil
		}
	}
	return dockerapi.Container{}, false, nil
}

// logsPage is the full-page log view; app.js fills it from logsStream.
func (s *Server) logsPage(w http.ResponseWriter, r *http.Request) {
	c, ok, err := s.findContainer(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, "listing containers: "+err.Error(), http.StatusBadGateway)
		return
	}
	if !ok {
		http.Error(w, "no such container", http.StatusNotFound)
		return
	}
	details, _ := s.deps.Runtime.Inspect(r.Context(), c.ID)
	chrome, _ := s.dashboard(r.Context(), r, "logs")
	stack := ""
	for _, g := range chrome.Groups {
		for _, gc := range g.Containers {
			if gc.ID == c.ID {
				stack = g.Stack.Name
			}
		}
	}
	data := struct {
		Hostname string
		C        dockerapi.Container
		D        dockerapi.Details
		ExitCode string
		Tail     int
		Tails    []int
		Index    indexData
		Stack    string
	}{s.deps.Hostname, c, details, exitCode(c.Status), LogTail, LogTails, chrome, stack}
	render(w, http.StatusOK, "logs.html", data)
}

// logsStream sends the container's last LogTail lines as Server-Sent Events, then
// follows new output while the container runs, and ends with a "done" event.
func (s *Server) logsStream(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, ok, err := s.findContainer(ctx, r.PathValue("id"))
	if err != nil {
		http.Error(w, "listing containers: "+err.Error(), http.StatusBadGateway)
		return
	}
	if !ok {
		http.Error(w, "no such container", http.StatusNotFound)
		return
	}
	details, err := s.deps.Runtime.Inspect(ctx, c.ID)
	if err != nil {
		http.Error(w, "inspecting container: "+err.Error(), http.StatusBadGateway)
		return
	}
	sse(w, r, func(send func(event, data string) error) error {
		opts := dockerapi.LogOptions{Tail: logTail(r), Follow: c.State == "running", Tty: details.Tty,
			Timestamps: r.URL.Query().Get("timestamps") == "1"}
		err := s.deps.Runtime.Logs(ctx, c.ID, opts, func(line string) error {
			return send("", line)
		})
		if err != nil && ctx.Err() == nil {
			send("error", err.Error())
		}
		return send("done", "")
	})
}

// addUnits fills in each Quadlet Stack's systemd units and their states.
func (s *Server) addUnits(ctx context.Context, d *view.Dashboard) {
	var all []string
	for _, g := range d.Groups {
		if g.Stack.Kind == stacks.Quadlet {
			all = append(all, g.Stack.Services()...)
		}
	}
	states, err := s.deps.Units.States(ctx, all)
	if err != nil {
		slog.Error("reading unit states", "err", err)
	}
	for i, g := range d.Groups {
		if g.Stack.Kind != stacks.Quadlet {
			continue
		}
		for _, svc := range g.Stack.Services() {
			st, ok := states[svc]
			if !ok {
				st = systemd.UnitState{Name: svc, ActiveState: "unknown"}
			}
			d.Groups[i].Units = append(d.Groups[i].Units, st)
		}
	}
}

// knownUnit reports whether unit belongs to a Quadlet Stack, so the unit-log views only
// ever read scd's own units.
func (s *Server) knownUnit(ctx context.Context, unit string) bool {
	if s.deps.Units == nil || s.deps.Repo == nil {
		return false
	}
	ss, _, err := s.discover(ctx)
	if err != nil {
		return false
	}
	for _, st := range ss {
		if st.Kind == stacks.Quadlet && slices.Contains(st.Services(), unit) {
			return true
		}
	}
	return false
}

func (s *Server) unitLogsPage(w http.ResponseWriter, r *http.Request) {
	unit := r.PathValue("unit")
	if !s.knownUnit(r.Context(), unit) {
		http.Error(w, "no Quadlet unit named "+unit, http.StatusNotFound)
		return
	}
	chrome, _ := s.dashboard(r.Context(), r, "logs")
	data := struct {
		Hostname, Unit string
		Tail           int
		Tails          []int
		Index          indexData
	}{s.deps.Hostname, unit, LogTail, LogTails, chrome}
	render(w, http.StatusOK, "unitlogs.html", data)
}

// unitLogsStream streams a Quadlet unit's journal (last LogTail lines, then live) as
// Server-Sent Events.
func (s *Server) unitLogsStream(w http.ResponseWriter, r *http.Request) {
	unit := r.PathValue("unit")
	if !s.knownUnit(r.Context(), unit) {
		http.Error(w, "no Quadlet unit named "+unit, http.StatusNotFound)
		return
	}
	sse(w, r, func(send func(event, data string) error) error {
		lw := &lineWriter{send: func(line string) error { return send("", line) }}
		err := s.deps.Units.Journal(r.Context(), unit, logTail(r), true, lw)
		lw.Flush()
		if err != nil && r.Context().Err() == nil {
			send("error", err.Error())
		}
		return send("done", "")
	})
}

// lineWriter turns written output into one send per complete line.
type lineWriter struct {
	mu      sync.Mutex
	send    func(string) error
	partial string
}

func (l *lineWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	parts := strings.Split(l.partial+string(p), "\n")
	l.partial = parts[len(parts)-1]
	for _, line := range parts[:len(parts)-1] {
		if err := l.send(strings.TrimRight(line, "\r")); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// Flush sends any final unterminated line.
func (l *lineWriter) Flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.partial != "" {
		l.send(l.partial)
		l.partial = ""
	}
}

func (s *Server) missing() []string {
	if s.deps.Missing == nil {
		return []string{}
	}
	if m := s.deps.Missing(); m != nil {
		return m
	}
	return []string{}
}

func (s *Server) newRelease() string {
	if s.deps.NewerRelease == nil {
		return ""
	}
	return s.deps.NewerRelease()
}

// render executes a page into a buffer first, so a template error becomes a clean 500
// instead of a page cut off halfway.
func render(w http.ResponseWriter, status int, name string, data any) {
	var buf bytes.Buffer
	if err := pages.ExecuteTemplate(&buf, name, data); err != nil {
		slog.Error("rendering page", "page", name, "err", err)
		http.Error(w, "rendering "+name+": "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

// truthy follows template truthiness for the ifelse helper.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case int:
		return x != 0
	case string:
		return x != ""
	case []view.Problem:
		return len(x) > 0
	}
	return true
}

// relTime is a friendly local time: "today 18:22", "yesterday 18:22", "Mon 18:22", or a date.
func relTime(t time.Time) string {
	now := time.Now().Local()
	t = t.Local()
	day := func(x time.Time) time.Time { y, m, d := x.Date(); return time.Date(y, m, d, 0, 0, 0, 0, x.Location()) }
	switch days := int(day(now).Sub(day(t)).Hours() / 24); {
	case days == 0:
		return "today " + t.Format("15:04")
	case days == 1:
		return "yesterday " + t.Format("15:04")
	case days > 1 && days < 7:
		return t.Format("Mon 15:04")
	}
	return t.Format("2 Jan 2006 15:04")
}

// took renders a duration compactly: "850ms", "12s", "3m 05s".
func took(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// historyOutput shows the kept output of one Apply.
func (s *Server) historyOutput(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	st, err := s.deps.State.Get()
	if err != nil {
		http.Error(w, "reading state: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var rec *state.ApplyRecord
	for i := range st.History {
		if st.History[i].ID == id && id != "" {
			rec = &st.History[i]
		}
	}
	if rec == nil || s.deps.Engine == nil {
		http.Error(w, "no Apply "+id+" in the history", http.StatusNotFound)
		return
	}
	lines, ok, err := s.deps.Engine.Outputs.Load(id)
	if err != nil {
		http.Error(w, "reading Apply output: "+err.Error(), http.StatusInternalServerError)
		return
	}
	chrome, _ := s.dashboard(r.Context(), r, "history")
	render(w, http.StatusOK, "applyoutput.html", struct {
		Hostname string
		Rec      *state.ApplyRecord
		Lines    []string
		Kept     bool
		Index    indexData
	}{s.deps.Hostname, rec, lines, ok, chrome})
}

// exitCode extracts N from a status like "Exited (N) 3 hours ago".
func exitCode(status string) string {
	rest, ok := strings.CutPrefix(status, "Exited (")
	if !ok {
		return ""
	}
	code, _, _ := strings.Cut(rest, ")")
	return code
}

// sicon picks a container row's status icon: ok, err (needs attention), pend
// (starting or restarting) or off (stopped cleanly or not started).
func sicon(c view.Container) string {
	switch {
	case c.NeedsAttention():
		return "err"
	case c.State == "running" && c.Health == "starting", c.State == "restarting":
		return "pend"
	case c.State == "running":
		return "ok"
	}
	return "off"
}

// navCount reads a side-navigation count from the dashboard data (-1 = don't show).
func navCount(v any, name string) int {
	if d, ok := v.(indexData); ok && d.Counts != nil {
		return d.Counts[name]
	}
	return -1
}

// backTo is where Sync and Apply return to: the Overview if they were pressed there,
// otherwise the Deployment page, which shows the plan.
func backTo(r *http.Request, anchor string) string {
	if ref, err := url.Parse(r.Referer()); err == nil && ref.Path == "/" {
		return "/" + anchor
	}
	return "/deployment" + anchor // the Sync Plan and Apply live there
}
