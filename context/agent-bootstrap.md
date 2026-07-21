# Agent Bootstrap

Use this document when changing repository-controlled agent hooks, session
startup, or automatic frontend dependency installation.

Repo-controlled `SessionStart` hooks may bootstrap frontend dependencies when
local tooling cannot run without them. This reduces execution surface but does
not fully protect a runner that blindly trusts changed hook configuration.

- Keep the bootstrap command self-contained in the hook definition; do not
  delegate to branch-controlled helper scripts or Makefile targets.
- Prefer an existing `node_modules/vite-plus/bin/vp`.
- When installation is required, use a frozen lockfile with lifecycle scripts
  disabled through `--ignore-scripts`, then verify the local Vite+ entrypoint
  exists.
