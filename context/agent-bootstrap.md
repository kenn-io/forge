# Agent Bootstrap

Use this document only for committed repository hooks that bootstrap frontend
dependencies at session start. For kenn-forge's user-level lifecycle-hook relay
or generated launch context, read [`workspace-apis.md`](./workspace-apis.md).

Repo-controlled `SessionStart` hooks may bootstrap frontend dependencies when
local tooling cannot run without them. This reduces execution surface but does
not fully protect a runner that blindly trusts changed hook configuration
(`.claude/settings.json:8`, `.codex/hooks.json:8`).

- Keep the bootstrap command self-contained in the hook definition; do not
  delegate to branch-controlled helper scripts or Makefile targets.
- Prefer an existing `node_modules/vite-plus/bin/vp`.
- When installation is required, use a frozen lockfile with lifecycle scripts
  disabled through `--ignore-scripts`, then verify the local Vite+ entrypoint
  exists.
