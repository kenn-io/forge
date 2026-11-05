---
name: forge-mcp-hill-climb
description: Use only when the user explicitly invokes /forge-mcp-hill-climb to re-tune the Forge MCP agent-facing text against the private replay eval; never invoke on your own.
disable-model-invocation: true
---

# Forge MCP hill climb

Re-tune the text Forge MCP puts in front of agents (server instructions, tool
descriptions, schema prose, the workspace context file) against a replay eval of real
agent mistakes, then cut it down without losing the score. Use the general `hill-climb`
skill for method; this skill holds what is specific to Forge.

Run it only on explicit request. It costs real model spend (a full eval is about 40
minutes and roughly $40 of API-equivalent usage) and reads the maintainer's agent
history.

## Where things live

- **Private eval directory** (`$FORGE_MCP_EVAL_DIR`, default `~/hill-climb/forge-mcp`).
  It holds the audit (`report.md`), mining scripts (`scripts/`, `sql/`, `data/`), the
  eval (`eval/`: `run.py`, `fakeforge.py`, `world.py`, `checks.py`, `tokens.py`,
  `dump.sh`, `cases/`, `cases_holdout/`, and a `README.md` on sandboxing and models),
  results, `iterations.md` and the PR draft. Its cases come from the maintainer's own
  sessions. **Never copy any of it into this repository**, and keep case names, prompts
  and session details out of commits and PRs. If the directory is missing, stop and ask;
  rebuilding it means a new Phase 0 audit.
- **Agent-facing text in this repo:**
  - Server instructions: `serverInstructions` in `internal/mcpserver/guidance.go`.
  - Tool descriptions: the `Description` strings registered with `addTool` in
    `internal/mcpserver/tools_*.go`, `apps.go` and `media.go`.
  - Schema prose: `jsonschema` tags in `internal/mcpserver/types.go` and
    `worklist/worklist.go`; literal hints such as `itemIdentityHint` in `tools_items.go`.
  - The workspace context file: `internal/workspace/agent_context.go`
    (`BuildAgentContext`, `RenderAgentContext`).

## Phases and gates

Commit locally after each phase. Do not push or open a PR without the user's go-ahead.

0. **Audit.** Only if the user asks for a fresh one or behavior has clearly drifted:
   re-run the mining scripts over recent history and update `report.md` with recurring
   mistakes and counts. Each new eval case cites its evidence in `source`.
1. **Eval.** Cases are `bad` (reproduces a real mistake), `guard` (must keep working),
   or `control` (ordinary task, no Forge tool). Keep the holdout frozen; never tune on it.
   Check that `origin/main` fails the `bad` cases before trusting them.
2. **Score.** Change the text until Opus and Sol pass every tuned case (3 samples).
3. **Cut.** Reduce prose tokens to a third of `main` or less while keeping the Phase 2
   score. Measure with `tokens.py` against a dump of `main`.

Commands, from `$FORGE_MCP_EVAL_DIR/eval`:

```sh
../.venv/bin/python -I run.py --checkout <worktree> --label <L> --models opus,sol,grok --samples 3 --jobs 6
../.venv/bin/python -I run.py --checkout <worktree> --label <L>-holdout --models opus,sol,grok --samples 3 --jobs 6 --holdout
../.venv/bin/python -I run.py --checkout <worktree> --label <L> --cases a,b      # affected cases only
./dump.sh <main-worktree> > /tmp/d-main.json
./dump.sh <worktree> > /tmp/d.json && ../.venv/bin/python -I tokens.py /tmp/d-main.json /tmp/d.json
```

Results land in `../results/<label>/summary.md`. Log every round in `iterations.md`.

## Rules learned the hard way

- `run.py` dumps the text from the checkout when it starts. Restart it after any edit.
- Classify every score change as **fixed**, **real regression**, or **not caused by
  this change**, with transcript evidence. With three samples per case, a single-sample
  swing is noise until it repeats.
- Opus 429s show up as harness errors, not failures. Lower `--jobs` and rerun the label;
  finished runs are reused.
- Codex (Sol) sometimes batches a `gh` call with tool discovery before any Forge text is
  visible. No wording fixes it; do not chase it.
- In Codex code mode the server instructions are prepended to every Forge tool, so every
  instruction token is multiplied by the tool count. Cut instructions first.
- grok loads no server instructions and no workspace context file. Treat it as observed
  only; its score shows what tool descriptions alone achieve.
- Name concrete actions and conditions ("read with Forge first; use `gh` after a push, for
  CI logs, ..."), not preferences ("prefer Forge"). Preferences lose to harness defaults.
- Show literal argument objects (the `item` example) where agents guess field shapes.
- Key retry rules on `kenn_forge_spawn_workspace_with_agent` to facts the error carries
  (kind, `workspace_id`, `ambiguous`), checked against `handoffFailure` in
  `tools_agent_spawn.go`. Looser wording came back from review three times.
- Do not cut provider facts that only matter off GitHub (`platform_host` for self-hosted
  hosts, Bitbucket Cloud UUIDs). The eval covers GitHub only, so it cannot see the loss.
- If the real `kenn-forge` binary is on the sandbox `PATH`, agents may run it and fail;
  count that as an eval leak, not a text regression.

## Review before the PR

- Put the cut text through the council (GPT-6.1 Sol xhigh and Claude Opus 5.5 high, via
  `delegate_task`) with the before/after diff and the classified misses. Confirm each
  finding against the code before changing anything.
- Merge `origin/main` and rerun the full eval plus holdout on the merged tree; main
  often rewrites the same registration lines.
- `go test ./internal/mcpserver/... ./internal/workspace/ ./worklist/...` and
  `make lint-check` must pass.

## PR

Follow `context/pull-request-workflow.md`. The body reports tuned and holdout scores per
model for `main` and the branch, the token table, the remaining misses with causes, and
the limits (fake server, GitHub only, regex checks, 3 samples). A chart of the scores
may be attached with the `capture-playwright` skill after the user approves the upload.
Describe the eval only in aggregate.
