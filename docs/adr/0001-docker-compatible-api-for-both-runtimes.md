# One Docker-compatible API client for both Docker and Podman

The dashboard reads container state through the Docker-compatible REST API only, which Podman also serves on its socket, so one client covers both runtimes. We accept losing Podman-only details (pod membership, auto-update policy, infra containers) in exchange for a single code path; Quadlet Stacks are grouped by systemd unit labels instead of pods. If pod-level detail is ever needed, add a narrow libpod call rather than a second backend.
