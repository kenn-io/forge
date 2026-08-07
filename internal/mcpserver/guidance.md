# Kenn Forge MCP Guidance

Use kenn-forge's MCP companion as a cached maintainer console. The companion
reads from the running daemon and does not force provider refreshes.

Recommended flow:

1. Call `kenn_forge_list_repos` first to discover valid repo filters and sync
   freshness.
2. Use `kenn_forge_find_review_candidates` to find recent PR and issue activity.
3. Inspect details only for plausible items with `kenn_forge_get_item_context`.
4. Use `kenn_forge_get_item_diff` to check the size and shape of a PR before
   claiming it. Request a full diff file only when the summary is not enough.
5. Consult `kenn_forge_get_stack_context` before claiming a stacked PR so review
   order respects the stack.
6. Prefer cached evidence over assumptions, and report stale cache signals or
   uncertainty.
7. Avoid provider writes. The only MCP write is
   `kenn_forge_set_item_workflow_state`, which changes kenn-forge-local workflow
   state.
8. Set workflow state only when the reason is clear. Include `expected_status`
   when marking an item so a stale agent run does not overwrite humans or other
   agents. Use `force: true` only for a deliberate unconditional local
   override.
9. Treat `awaiting_merge` as a PR-oriented state. Avoid setting it on issues
   unless the user explicitly asks for that state.

Example guidance flow:

```text
1. Call kenn_forge_find_review_candidates with since equal to the scheduler's
   last successful run.
2. For the top candidates, call kenn_forge_get_item_context.
3. Decide whether the activity needs human or agent review.
4. If claiming the item, call kenn_forge_set_item_workflow_state with
   status="reviewing", expected_status from the candidate row, and a short
   reason.
5. Report what was claimed and what was skipped.
```
