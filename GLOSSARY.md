# Simple Container Dashboard

A lightweight, mostly read-only web dashboard for the containers on one host (Docker or rootless Podman), plus a single write action that brings the host in line with a git repo of Stack definitions.

## Language

### Deployment

**Deployment Repo**:
The git repository holding the Stack definitions for a host, and its clone on that host.
_Avoid_: compose repo, quadlet repo, config repo

**Stack**:
A deployable unit: one top-level directory of the Deployment Repo, named after the directory.
_Avoid_: app, project, service

**Compose Stack**:
A Stack defined by a `compose.yaml`.

**Quadlet Stack**:
A Stack defined by Podman Quadlet unit files.

**Selected Stack**:
A Stack chosen to run on this host. Stacks are unselected until chosen, and an unselected Stack is never deployed.
_Avoid_: enabled, active

**Invalid Stack**:
A Stack whose definition fails validation at the commit being planned; it is skipped, with the reason shown.

**Secrets File**:
A host-local `secrets.env` inside a Stack's directory on the host, never committed to the Deployment Repo and never modified by scd, supplying secrets to that Stack when it is deployed.
_Avoid_: env file, .env, stack env file

**Sync**:
Fetching the Deployment Repo, validating it and producing a Sync Plan. A Sync changes nothing on the host.
_Avoid_: pull, refresh

**Sync Plan**:
The per-Stack list of deploys, redeploys and teardowns needed to bring the host in line with one specific commit and the current selection. It becomes stale when the selection changes.
_Avoid_: diff, dry run

**Apply**:
Carrying out a Sync Plan on the host. Only possible while a valid, non-stale Sync Plan exists.
_Avoid_: deploy, update

### Containers

**Managed Container**:
A container belonging to a Stack that Sync deployed and may therefore redeploy or tear down.

**Unmanaged Container**:
Any container Sync did not deploy; shown in the dashboard but never touched.
_Avoid_: foreign, external
