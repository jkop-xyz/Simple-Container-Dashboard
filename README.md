# Simple Container Dashboard (scd)

A tiny, mostly read-only web dashboard for the containers on one host, working with both **Docker** and **rootless Podman**, plus one carefully scoped write action: keeping the host in line with a git repo of Compose or Quadlet definitions.

> [!NOTE]
> **This project is completely AI-made.** The design, code, tests and documentation are written by an AI model ([Claude](https://www.anthropic.com/claude), by Anthropic) working under the direction of the maintainer, who sets requirements, makes the design decisions and reviews the results. Treat it as you would any young open-source project: read before you run.

> [!IMPORTANT]
> **Status: in development.** Nothing is released yet. The design is agreed and tracked in [issue #1](https://github.com/jkop-xyz/Simple-Container-Dashboard/issues/1), with the work split into tickets ([#2–#19](https://github.com/jkop-xyz/Simple-Container-Dashboard/issues)).

## Why

If you run a handful of small hosts and keep their container definitions in git, the day-to-day questions are usually simple: *What's running? Why did that crash? Which port was that UI on? Is this host up to date with the repo?* Answering them usually means SSHing in and typing commands, while existing dashboards tend to be heavy, need broad write access, or don't know anything about "deploy what's in my repo".

scd aims to be the small thing you install once per host and mostly just look at.

## What it does

**Read-only view**
- Every container on the host, grouped by Stack, including stopped, exited and failing ones
- Health status, restart count, CPU and memory, refreshed every few seconds
- Published ports as clickable links that open `http://<the hostname you used>:<port>` in a new tab
- Logs: the last 500 lines, then a live follow
- On Podman hosts: the systemd state and journal of each Quadlet unit, so units that fail before creating a container are still visible

**Sync and Apply** (the only write workflow)
- Your **Deployment Repo** holds one directory per **Stack**: a `compose.yaml` on Docker hosts, or Quadlet files on Podman hosts
- Tick which Stacks should run on this host
- **Sync** fetches the repo, validates it and shows a **Sync Plan**: which Stacks will be deployed, redeployed or torn down, and why
- **Apply** carries out exactly that plan, continues past failures, and reports what went wrong at the end
- Volumes are never deleted, and images for unchanged Stacks are never updated (that's left to tools like Renovate and Podman auto-update)
- Secrets live in a git-ignored `secrets.env` next to each Stack on the host. scd reads it and never writes it.

## Design goals

- **Light**: a single Go binary running as a `systemctl --user` service, with the web UI embedded. It calls the tools already on the host (`git`, `docker compose`, `podman`, `systemctl`) rather than reimplementing them.
- **Easy to install**: download the binary and run `scd setup`, which installs missing tools, handles GitHub auth, lets you pick your repo and writes the service unit.
- **Safe by default**: scd only touches Stacks it deployed itself, never resets your clone, and always shows you what it will do before it does it.

## Planned installation

Once the first release is out:

1. Download the binary for your architecture (linux/amd64 or linux/arm64) from Releases into `~/.local/bin/scd`.
2. Run `scd setup` and follow the prompts.
3. Enable and start the service using the commands setup prints, then open `http://<host>:1234`.

Setup supports dnf- and apt-based distributions (Fedora, RHEL, Debian, Ubuntu and friends). Elsewhere it tells you what to install by hand.

## Not in scope (for now)

Authentication (run it on a trusted network for now), start/stop buttons, webhooks or scheduled syncs, multi-host overviews, and encrypted secrets. See the spec for the full list.

## Project docs

- [GLOSSARY.md](GLOSSARY.md): the project's vocabulary (Stack, Sync, Apply, ...)
- [docs/adr/](docs/adr/): architecture decisions and why they were made
