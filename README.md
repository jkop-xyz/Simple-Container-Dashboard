# Simple Container Dashboard (scd)

A tiny, mostly read-only web dashboard for the containers on one host, working with both **Docker** and **rootless Podman**, plus one carefully scoped write action: keeping the host in line with a git repo of Compose or Quadlet definitions.

> [!NOTE]
> **This project is completely AI-made.** The design, code, tests and documentation are written by an AI model ([Claude](https://www.anthropic.com/claude), by Anthropic) working under the direction of the maintainer, who sets requirements, makes the design decisions and reviews the results. Treat it as you would any young open-source project: read before you run.

## What it does

**Read-only view**
- Every container on the host, grouped by Stack, including stopped, exited and failing ones
- Health, restart count, CPU and memory, refreshed every 5 seconds (paused while the tab is hidden)
- Published ports as links that open `http://<the hostname you used>:<port>` in a new tab
- Logs: the last 500 lines, then a live follow you can pause
- On Podman hosts: each Quadlet unit's systemd state and journal, so units that fail before creating a container are still visible, plus whether `podman-auto-update.timer` is active

**Sync and Apply**, the only write workflow
- Tick which Stacks of your Deployment Repo should run on this host. New Stacks start unticked.
- **Sync** fetches the repo, validates it and shows a **Sync Plan**: which Stacks will be deployed, redeployed or torn down, and why. Sync itself changes nothing.
- **Apply** carries out exactly that plan. It continues past failures, reports them at the end, and retries failed Stacks on the next Sync.
- Volumes are never deleted. Images of unchanged Stacks are never updated; that's left to Renovate and Podman auto-update ([ADR 0002](docs/adr/0002-sync-is-repo-driven-and-never-deletes-data.md)).
- The last 20 Applies are kept in a history view.

## Install

scd is a single static binary for Linux (amd64 and arm64) that runs as a `systemctl --user` service.

1. Download the binary for your machine from [Releases](https://github.com/jkop-xyz/Simple-Container-Dashboard/releases) and put it on your `PATH`:

   ```sh
   mkdir -p ~/.local/bin
   curl -fL -o ~/.local/bin/scd \
     https://github.com/jkop-xyz/Simple-Container-Dashboard/releases/latest/download/scd-linux-amd64   # or scd-linux-arm64
   chmod +x ~/.local/bin/scd
   ```

   Each release has a `SHA256SUMS` file for checking the download.

2. Run the interactive setup as the user that should own the containers:

   ```sh
   scd setup
   ```

3. Start the service with the commands setup prints, then open `http://<host>:1234`:

   ```sh
   systemctl --user daemon-reload
   systemctl --user enable --now scd.service
   ```

### What `scd setup` does

It asks before each step that changes the system:

1. **Runtime.** Detects Docker or Podman, and asks if both or neither are installed.
2. **Tools.** Installs whichever of `git`, `gh`, `podman` is missing, with `dnf` or `apt`. Docker is installed with Docker's [convenience script](https://get.docker.com) on Debian, Ubuntu, Raspbian, Fedora, RHEL and CentOS. Setup shows the script's commands before running them, and Docker itself describes the script as meant for quick setups rather than production. On other distributions, setup only tells you what to install.
3. **GitHub.** Runs `gh auth login` (if needed) and `gh auth setup-git`, then lets you pick the Deployment Repo from a filterable list of your own repos. The repo's default branch is used, and setup clones the repo straight away, so authentication problems show up during setup.
4. **Files.** Writes `~/.config/scd/config.toml` and `~/.config/systemd/user/scd.service`.
5. **Runtime access.**
   - Docker: adds you to the `docker` group and enables `docker.service`. Log out and back in before starting scd.
   - Podman: enables the user `podman.socket` and `podman-auto-update.timer`, and turns on lingering, so your services survive logout.
6. **Firewall.** If firewalld or ufw is active, offers to open port 1234/tcp.

Setup never starts scd itself.

### Using a deploy key instead of `gh`

The `gh` login gives scd your account's access to every repo you can see. For a host you care about, a read-only deploy key that can read only the Deployment Repo is tighter:

```sh
ssh-keygen -t ed25519 -f ~/.ssh/scd_deploy -N "" -C "scd@$(hostname)"
cat ~/.ssh/scd_deploy.pub   # add under the repo's Settings → Deploy keys (leave "Allow write access" off)
cat >> ~/.ssh/config <<'CONF'
Host github.com
  IdentityFile ~/.ssh/scd_deploy
  IdentitiesOnly yes
CONF
```

Then set `url = "git@github.com:<owner>/<repo>.git"` under `[repo]` in the config. scd only ever runs plain `git`, so nothing else changes.

### Updating scd

Download the new binary over the old one and restart the service:

```sh
systemctl --user restart scd.service
```

The dashboard header shows a hint when a newer release exists. It checks GitHub Releases at most once a day, and the request carries no information about your host.

## The Deployment Repo

One directory per **Stack**, named after the Stack. A host's repo is either all Compose Stacks (Docker hosts) or all Quadlet Stacks (Podman hosts).

```
homelab/
├── .gitignore          # must contain: secrets.env
├── jellyfin/
│   ├── compose.yaml    # Compose Stack (Docker hosts)
│   └── secrets.env     # on the host only, never committed
└── traefik/
    └── compose.yaml
```

```
homelab-quadlet/
├── .gitignore          # must contain: secrets.env
├── jellyfin/
│   ├── jellyfin.container
│   ├── media.volume
│   └── config/app.ini  # supporting files are deployed alongside the units
└── traefik/
    ├── traefik.container
    └── proxy.network
```

Rules (a Stack that breaks one shows as **Invalid** in the Sync Plan and is skipped):

- **One definition per Stack.** A Stack has either a `compose.yaml` or Quadlet files (`.container`, `.pod`, `.network`, `.volume`, `.kube`, `.image`, `.build`). Both or neither makes it Invalid.
- **Names become project names.** A Stack name must work as a compose project name. Names are lowercased (`App1` runs as `app1`), and two names that collide after lowercasing are both Invalid.
- **Quadlet unit names must be unique across the whole repo**, because systemd has a single namespace (`a/web.container` and `b/web.container` would both be `web.service`).
- **Ignored paths.** Hidden directories (`.github/`, `.renovate/`) and files at the repo root are ignored.
- **Definitions must validate.** Compose Stacks must pass `docker compose config`, and Quadlet Stacks must pass Podman's quadlet generator in dry-run mode. Quadlet Stacks need Podman 4.8 or newer.

### How Stacks are deployed

- **Compose:** `docker compose --project-name <stack> … up --detach --remove-orphans`, run from the clone. Teardown runs `down --remove-orphans`, without `--volumes`.
- **Quadlet:** the Stack's tracked files (everything except `secrets.env`) are copied to `~/.config/containers/systemd/<stack>/`. Then `systemctl --user daemon-reload` runs and the Stack's units are restarted. Units whose files were removed are stopped first. Teardown stops the units and removes only that subdirectory. Quadlet files you put elsewhere in that directory are never touched.

### Secrets

Secrets live in a hand-placed `secrets.env` inside the Stack's directory **in the clone on the host**. It is never committed and never modified by scd ([ADR 0003](docs/adr/0003-secrets-live-untracked-inside-the-clone.md)).

- **Compose:** passed as `--env-file`, so `${VARIABLES}` in `compose.yaml` are filled from it.
- **Quadlet:** scd generates a drop-in, `<unit>.container.d/scd-secrets.conf`, that sets `EnvironmentFile=` to the file in the clone. The secret itself is never copied.
- **Rotation:** changing the file plans a "redeploy (secrets changed)".
- **Safety:** Sync refuses to run unless the repo's root `.gitignore` contains `secrets.env`. A commit that tracks a `secrets.env` blocks the Apply, so the host's file is never overwritten.

The clone lives at `~/.local/share/scd/repo` by default.

### Container labels

| Label | Effect |
|---|---|
| `scd.scheme=https` | That container's port links open with `https://` |

## Configuration

`~/.config/scd/config.toml`, written by `scd setup`:

```toml
listen = ":1234"            # address the dashboard listens on
runtime = "docker"          # or "podman"; empty = auto-detect
# socket = "/run/user/1000/podman/podman.sock"   # override the runtime socket

[repo]
url = "https://github.com/you/homelab.git"
branch = "main"             # empty = the clone's branch
path = "/home/you/.local/share/scd/repo"
```

There is no authentication yet, so run scd on a network you trust.

## Manual verification checklist

The automated tests fake the runtime API and every external command. Before trusting a release on real hosts, check these on a throwaway VM:

- [ ] **Rootless Podman (Fedora or Debian)**
  - [ ] `scd setup` enables the socket, the auto-update timer and linger.
  - [ ] A Quadlet Stack deploys; its units and containers show up grouped together.
  - [ ] The unit log streams.
  - [ ] Teardown removes `~/.config/containers/systemd/<stack>/` and leaves the volume in `podman volume ls`.
  - [ ] Confirm that the minimum Podman version (4.8) for subdirectories and drop-ins is right.
- [ ] **Docker (Ubuntu)**
  - [ ] The convenience-script install works and the `docker` group change takes effect after re-login.
  - [ ] CPU and RAM show for running containers.
  - [ ] Compose Stacks deploy, redeploy and tear down, and volumes survive.
- [ ] **GitHub**
  - [ ] `gh auth login` with a private Deployment Repo.
  - [ ] The repo picker lists it.
  - [ ] Sync fetches over HTTPS through `gh`'s credential helper.
- [ ] **Firewall:** setup opens 1234/tcp in firewalld (Fedora) and ufw (Ubuntu), and the dashboard is reachable from another machine.
- [ ] **Port links** open on the hostname used in the browser.

## Development

```sh
go test ./...        # needs git; never touches Docker, Podman or systemd
go build ./cmd/scd
```

Design notes: [GLOSSARY.md](GLOSSARY.md) for the vocabulary, and [docs/adr/](docs/adr/) for the decisions behind it.
