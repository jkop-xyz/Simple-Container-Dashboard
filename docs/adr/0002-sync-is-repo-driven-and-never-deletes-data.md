# Sync and Apply act only on Deployment Repo changes and never delete volumes

A Sync Plan, and the Apply that carries it out, is driven purely by what changed in the Deployment Repo and the Stack selection. Apply never pulls newer images for unchanged Stacks (Renovate updates tags in the repo, and Podman auto-update handles Quadlet images), and tearing down a Stack never removes its volumes, because data lives on a remote NAS and a deleted directory in git must not destroy it. Anyone tempted to add "update images" or "prune volumes" to Apply should know these were left out on purpose.
