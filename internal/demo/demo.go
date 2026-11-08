// Package demo runs the real dashboard against an in-memory container runtime, an
// in-memory Deployment Repo and fake deploy commands, so the web interface can be tried
// without Docker, Podman, git or a repository. Nothing on the machine is touched.
package demo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/app"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/config"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/dockerapi"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/dockerapi/dockertest"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/engine"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/runner"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/stacks"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/state"
)

// DefaultListen keeps the demo on this machine unless asked otherwise.
const DefaultListen = "127.0.0.1:1234"

// service is one container a demo Stack runs.
type service struct {
	name     string
	ports    []dockertest.Port
	health   string
	restarts int
	https    bool
	cpu      float64 // typical CPU %
	memMiB   uint64
}

func tcp(port int) dockertest.Port {
	return dockertest.Port{IP: "0.0.0.0", PrivatePort: port, PublicPort: port, Type: "tcp"}
}

func local(port int) dockertest.Port {
	return dockertest.Port{IP: "127.0.0.1", PrivatePort: port, PublicPort: port, Type: "tcp"}
}

// services are what each demo Stack runs when deployed.
var services = map[string][]service{
	"jellyfin":      {{name: "server", ports: []dockertest.Port{tcp(8096)}, health: "healthy", cpu: 6, memMiB: 740}},
	"traefik":       {{name: "proxy", ports: []dockertest.Port{tcp(80), tcp(443), tcp(8080)}, https: true, cpu: 1, memMiB: 48}},
	"nextcloud":     {{name: "app", ports: []dockertest.Port{tcp(8081)}, health: "healthy", cpu: 3, memMiB: 310}, {name: "db", ports: []dockertest.Port{local(5432)}, cpu: 2, memMiB: 190}},
	"homeassistant": {{name: "core", ports: []dockertest.Port{tcp(8123)}, health: "unhealthy", restarts: 3, cpu: 9, memMiB: 420}},
	"uptime":        {{name: "kuma", ports: []dockertest.Port{tcp(3001)}, health: "starting", cpu: 2, memMiB: 120}},
	"wireguard":     {{name: "vpn", ports: []dockertest.Port{{IP: "0.0.0.0", PrivatePort: 51820, PublicPort: 51820, Type: "udp"}}, cpu: 0.5, memMiB: 12}},
}

func composeFile(image string, svcs []service) string {
	var b strings.Builder
	b.WriteString("services:\n")
	for _, s := range svcs {
		fmt.Fprintf(&b, "  %s:\n    image: %s\n    restart: unless-stopped\n", s.name, image)
		if len(s.ports) > 0 {
			b.WriteString("    ports:\n")
			for _, p := range s.ports {
				fmt.Fprintf(&b, "      - \"%d:%d/%s\"\n", p.PublicPort, p.PrivatePort, p.Type)
			}
		}
	}
	return b.String()
}

// Demo is one running demo: the fakes plus the wired-up dashboard.
type Demo struct {
	api     *dockertest.Server
	git     *fakeGit
	dir     string
	handler http.Handler

	mu     sync.Mutex
	cpu    map[string]float64
	mem    map[string]uint64
	totals map[string]dockertest.Stats
	pushes int
}

// New builds a demo in a temporary directory. Close removes it.
func New(version string) (*Demo, error) {
	dir, err := os.MkdirTemp("", "scd-demo-")
	if err != nil {
		return nil, err
	}
	api, err := dockertest.Start()
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	d := &Demo{api: api, git: &fakeGit{}, dir: dir,
		cpu: map[string]float64{}, mem: map[string]uint64{}, totals: map[string]dockertest.Stats{}}
	d.seedRepo()
	if err := d.seedState(); err != nil {
		d.Close()
		return nil, err
	}
	d.seedContainers()

	clone := filepath.Join(dir, "repo")
	a, err := app.New(app.Options{
		Config: config.Config{
			Listen: DefaultListen, Runtime: config.Docker, Socket: api.Socket,
			Repo: config.Repo{URL: "https://github.com/demo/homelab.git", Branch: "main", Path: clone},
		},
		Env:           &dockerapi.Env{Getenv: func(string) string { return "" }, Exists: func(string) bool { return false }},
		Runner:        &fakeRunner{demo: d, clone: clone},
		Hostname:      "demo-host",
		DataDir:       filepath.Join(dir, "data"),
		LookPath:      func(f string) (string, error) { return "/demo/bin/" + f, nil },
		Version:       version,
		LatestRelease: func(context.Context) (string, error) { return "", errors.New("demo mode is offline") },
		Demo:          true,
		WatchFor:      15 * time.Second, // a quicker post-Apply health check to watch
		SampleEvery:   5 * time.Second,  // charts fill up quickly
	})
	if err != nil {
		d.Close()
		return nil, err
	}
	if err := a.Start(context.Background()); err != nil {
		d.Close()
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /demo/push", d.handlePush)
	mux.Handle("/", a.Handler)
	d.handler = mux
	return d, nil
}

// Handler is the dashboard plus the demo's own endpoints.
func (d *Demo) Handler() http.Handler { return d.handler }

// Close stops the fake runtime and removes the demo's files.
func (d *Demo) Close() {
	d.api.Close()
	os.RemoveAll(d.dir)
}

// seedRepo creates the history: what's deployed, and one commit waiting upstream.
func (d *Demo) seedRepo() {
	initial := map[string]string{
		".gitignore":                 "secrets.env\n",
		"README.md":                  "# homelab\n\nCompose Stacks for the demo host.\n",
		"jellyfin/compose.yaml":      composeFile("jellyfin/jellyfin:10.9.11", services["jellyfin"]),
		"traefik/compose.yaml":       composeFile("traefik:v3.1", services["traefik"]),
		"nextcloud/compose.yaml":     composeFile("nextcloud:29", services["nextcloud"]),
		"homeassistant/compose.yaml": composeFile("ghcr.io/home-assistant/home-assistant:2024.10", services["homeassistant"]),
		"wireguard/compose.yaml":     composeFile("lscr.io/linuxserver/wireguard:1.0", services["wireguard"]),
	}
	d.git.add("initial Stacks", initial)
	d.git.push("bump jellyfin to 10.10.3, add uptime, retire nextcloud", func(f map[string]string) {
		f["jellyfin/compose.yaml"] = composeFile("jellyfin/jellyfin:10.10.3", services["jellyfin"])
		f["uptime/compose.yaml"] = composeFile("louislam/uptime-kuma:1.23", services["uptime"])
		delete(f, "nextcloud/compose.yaml")
		f["notes/ideas.md"] = "- try Quadlet on the new box\n" // not a Stack: shows up as Invalid
	})
	d.git.head = 0
}

// seedState marks the initial Stacks as selected and deployed, as if Applied earlier.
func (d *Demo) seedState() error {
	files := d.git.commits[0].files
	st := state.Open(filepath.Join(d.dir, "data", "state.json"))
	return st.Update(func(s *state.State) {
		s.Selected = []string{"homeassistant", "jellyfin", "nextcloud", "traefik"}
		s.Deployed = map[string]state.DeployedStack{}
		for _, name := range s.Selected {
			s.Deployed[name] = state.DeployedStack{Kind: stacks.Compose, Tree: tree(files, name)}
		}
		id := engine.NewApplyID(time.Now().Add(-26*time.Hour), d.git.commits[0].hash)
		engine.Outputs{Dir: filepath.Join(d.dir, "data", "applies")}.Save(id, []string{
			"==> Fast-forwarding the clone to " + d.git.commits[0].hash[:7],
			"==> deploy homeassistant (selected and not deployed yet)", " Container homeassistant-core-1  Started",
			"==> deploy jellyfin (selected and not deployed yet)", " Container jellyfin-server-1  Started",
			"==> deploy nextcloud (selected and not deployed yet)", " Container nextcloud-app-1  Started", " Container nextcloud-db-1  Started",
			"==> deploy traefik (selected and not deployed yet)", " Container traefik-proxy-1  Started",
			"==> Apply finished",
		})
		s.AddHistory(state.ApplyRecord{
			ID:   id,
			From: d.git.commits[0].hash, To: d.git.commits[0].hash,
			Commits: []state.Commit{{Hash: d.git.commits[0].hash, Subject: d.git.commits[0].subject}},
			Started: time.Now().Add(-26 * time.Hour), Finished: time.Now().Add(-26*time.Hour + 40*time.Second),
			HealthCheck: "done",
			Results: []state.StackResult{
				{Stack: "homeassistant", Action: "deploy", After: state.AfterUnhealthy},
				{Stack: "jellyfin", Action: "deploy", After: state.AfterHealthy},
				{Stack: "nextcloud", Action: "deploy", After: state.AfterHealthy},
				{Stack: "traefik", Action: "deploy", After: state.AfterHealthy},
			},
		})
	})
}

func (d *Demo) seedContainers() {
	for _, name := range []string{"homeassistant", "jellyfin", "nextcloud", "traefik"} {
		d.deploy(name, imageOf(d.git.commits[0].files[name+"/compose.yaml"]))
	}
	d.api.Update(func(cs []dockertest.Container) []dockertest.Container {
		return append(cs,
			dockertest.Container{ID: "c0ffee000001", Name: "scratch-shell", Image: "alpine:3.20", State: "exited",
				Status: "Exited (1) 3 hours ago", Tty: true, Logs: []string{"/ # apk add curl", "ERROR: unable to select packages", "/ # exit 1"}},
			dockertest.Container{ID: "c0ffee000002", Name: "adhoc-redis", Image: "redis:7", State: "running", Status: "Up 5 days",
				Ports: []dockertest.Port{local(6379)}, Logs: []string{"Ready to accept connections tcp"}},
		)
	})
	d.loadFor("c0ffee000002", 0.4, 9)
}

func imageOf(compose string) string {
	for _, line := range strings.Split(compose, "\n") {
		if img, ok := strings.CutPrefix(strings.TrimSpace(line), "image: "); ok {
			return img
		}
	}
	return "demo/unknown"
}

// deploy (re)creates a Stack's containers, as `compose up` would.
func (d *Demo) deploy(stack, image string) []string {
	d.teardown(stack)
	var out []string
	var add []dockertest.Container
	for _, s := range services[stack] {
		name := fmt.Sprintf("%s-%s-1", stack, s.name)
		id := fmt.Sprintf("%x", rand.Uint64())
		labels := map[string]string{"com.docker.compose.project": stack, "com.docker.compose.service": s.name}
		add = append(add, dockertest.Container{
			ID: id, Name: name, Image: image, State: "running", Status: "Up Less than a second",
			Labels: labels, Health: s.health, RestartCount: s.restarts, Ports: s.ports,
			Logs: bootLog(stack, s.name, image),
		})
		out = append(out, fmt.Sprintf(" Container %s  Started", name))
	}
	d.api.Update(func(cs []dockertest.Container) []dockertest.Container { return append(cs, add...) })
	for i, s := range services[stack] {
		d.loadFor(add[i].ID, s.cpu, s.memMiB)
	}
	return out
}

// teardown removes a Stack's containers, as `compose down` would.
func (d *Demo) teardown(stack string) []string {
	var out, gone []string
	d.api.Update(func(cs []dockertest.Container) []dockertest.Container {
		var keep []dockertest.Container
		for _, c := range cs {
			if c.Labels["com.docker.compose.project"] == stack {
				out = append(out, fmt.Sprintf(" Container %s  Removed", c.Name))
				gone = append(gone, c.ID)
				continue
			}
			keep = append(keep, c)
		}
		return keep
	})
	for _, id := range gone {
		d.api.EndLogs(id) // open log views of removed containers end
	}
	return out
}

func bootLog(stack, svc, image string) []string {
	now := time.Now().Add(-2 * time.Minute)
	lines := []string{
		fmt.Sprintf("%s [%s] starting %s", now.Format(time.RFC3339), svc, image),
		fmt.Sprintf("%s [%s] loading configuration from /config", now.Add(time.Second).Format(time.RFC3339), svc),
	}
	for i := 0; i < 40; i++ {
		lines = append(lines, fmt.Sprintf("%s [%s] %s", now.Add(time.Duration(2+i)*time.Second).Format(time.RFC3339), svc, chatter(stack, i)))
	}
	return lines
}

func chatter(stack string, i int) string {
	msgs := map[string][]string{
		"jellyfin":      {"Scanning media library", "Transcoding job finished", "GET /System/Info 200", "Sending notification"},
		"traefik":       {"GET /api/overview 200", "Configuration loaded from provider docker", "TLS certificate renewed"},
		"nextcloud":     {"cron.php finished", "GET /status.php 200", "Background job executed"},
		"homeassistant": {"Error setting up integration: timeout", "Setup of sensor platform took longer than 10s", "Retrying in 30 seconds"},
		"uptime":        {"Monitor #3 is UP", "Monitor #1 is UP", "Sent heartbeat"},
		"wireguard":     {"Peer handshake completed", "Keepalive"},
	}
	m := msgs[stack]
	if len(m) == 0 {
		m = []string{"tick"}
	}
	return m[i%len(m)]
}

// loadFor sets a container's typical load and publishes a first reading that already
// carries the runtime's previous sample, so CPU shows from the first refresh.
func (d *Demo) loadFor(id string, cpu float64, memMiB uint64) {
	const window, cpus = 8e9, 4
	st := dockertest.Stats{
		CPUTotal: 10e9, SystemCPU: 100e9, OnlineCPUs: cpus,
		PreCPUTotal: 10e9 - uint64(cpu/100*window/cpus), PreSystemCPU: 100e9 - window,
		MemoryUsage: memMiB << 20,
	}
	d.mu.Lock()
	d.cpu[id], d.mem[id], d.totals[id] = cpu, memMiB, st
	d.mu.Unlock()
	d.api.SetStats(id, st)
}

// Animate keeps stats and logs moving until ctx ends.
func (d *Demo) Animate(ctx context.Context) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if i%15 == 7 { // homeassistant keeps falling over, so the crash-loop warning has something to show
			d.api.Update(func(cs []dockertest.Container) []dockertest.Container {
				for j := range cs {
					if cs[j].Labels["com.docker.compose.project"] == "homeassistant" {
						cs[j].RestartCount++
						cs[j].Status = "Up 4 seconds"
					}
				}
				return cs
			})
		}
		for _, c := range d.api.Containers() {
			if c.State != "running" {
				continue
			}
			d.mu.Lock()
			st := d.totals[c.ID]
			pct := d.cpu[c.ID] * (0.6 + rand.Float64()*0.8)
			// The dashboard shows ΔCPU/ΔSystem × CPUs × 100, so this yields pct.
			const window, cpus = 8e9, 4 // 2 s × 4 CPUs of system time, in ns
			st.SystemCPU += window
			st.CPUTotal += uint64(pct / 100 * window / cpus)
			st.PreCPUTotal, st.PreSystemCPU = 0, 0
			mem := float64(d.mem[c.ID]) * (0.95 + rand.Float64()*0.1)
			st.MemoryUsage = uint64(mem * (1 << 20))
			d.totals[c.ID] = st
			d.mu.Unlock()
			d.api.SetStats(c.ID, st)
			if rand.IntN(3) == 0 {
				stack := c.Labels["com.docker.compose.project"]
				svc := c.Labels["com.docker.compose.service"]
				d.api.AppendLogs(c.ID, fmt.Sprintf("%s [%s] %s", time.Now().Format(time.RFC3339), svc, chatter(stack, i)))
			}
		}
	}
}

// handlePush simulates someone pushing a commit to the Deployment Repo.
func (d *Demo) handlePush(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	d.pushes++
	n := d.pushes
	d.mu.Unlock()
	switch n % 3 {
	case 1:
		d.git.push(fmt.Sprintf("bump traefik to v3.%d", n+1), func(f map[string]string) {
			f["traefik/compose.yaml"] = composeFile(fmt.Sprintf("traefik:v3.%d", n+1), services["traefik"])
		})
	case 2:
		d.git.push("add wireguard tweaks", func(f map[string]string) {
			f["wireguard/compose.yaml"] = composeFile(fmt.Sprintf("lscr.io/linuxserver/wireguard:1.%d", n), services["wireguard"])
		})
	default:
		d.git.push(fmt.Sprintf("bump home-assistant to 2024.%d", 10+n), func(f map[string]string) {
			f["homeassistant/compose.yaml"] = composeFile(fmt.Sprintf("ghcr.io/home-assistant/home-assistant:2024.%d", 10+n), services["homeassistant"])
		})
	}
	slog.Info("demo: simulated a push to the Deployment Repo", "commits", n)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// fakeRunner answers every command scd would run: git from the in-memory repo, compose
// by changing the fake containers, anything else with success.
type fakeRunner struct {
	demo  *Demo
	clone string
}

func (f *fakeRunner) Run(ctx context.Context, cmd runner.Cmd) (runner.Result, error) {
	if cmd.Name == "git" {
		return f.demo.git.run(cmd)
	}
	line := cmd.String()
	if cmd.Name != "docker" || len(cmd.Args) == 0 || cmd.Args[0] != "compose" {
		return ok("")
	}
	if line == "docker compose version" {
		return ok("Docker Compose version v2.29.7 (demo)\n")
	}
	project := ""
	for i, a := range cmd.Args {
		if a == "--project-name" && i+1 < len(cmd.Args) {
			project = cmd.Args[i+1]
		}
	}
	var out []string
	switch last := cmd.Args[len(cmd.Args)-1]; {
	case last == "--quiet": // config validation
		return ok("")
	case strings.Contains(line, " up "):
		select { // look like real work, so the live output is worth watching
		case <-time.After(700 * time.Millisecond):
		case <-ctx.Done():
			return ok("")
		}
		image := imageOf(f.demo.git.latestFilesAtHead(project))
		out = append([]string{fmt.Sprintf(" Network %s_default  Created", project)}, f.demo.deploy(project, image)...)
	case strings.Contains(line, " down"):
		time.Sleep(400 * time.Millisecond)
		out = append(f.demo.teardown(project), fmt.Sprintf(" Network %s_default  Removed", project))
	}
	text := strings.Join(out, "\n") + "\n"
	if cmd.Output != nil {
		cmd.Output.Write([]byte(text))
	}
	return ok(text)
}

// latestFilesAtHead returns a Stack's compose.yaml at the clone's commit.
func (g *fakeGit) latestFilesAtHead(stack string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.commits[g.head].files[stack+"/compose.yaml"]
}
