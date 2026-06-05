/**
 * App-shell registration for the PR-detail palette commands.
 *
 * The canX/runX closures live in @middleman/ui
 * (`packages/ui/src/components/detail/keyboard-actions.ts`); registration
 * happens here in the app shell because the keyboard registry imports
 * Svelte 5 runes that the package can't reach upward for. The registry
 * itself does not know about the PR-detail action shape — it only sees
 * the wrapped Action entries this file produces.
 *
 * `pr.merge` is intentionally NOT registered in this pass. The existing
 * `runOpenMerge` closure flips a `setMergeModalOpen` flag that today is
 * owned by `PullDetail.svelte` as local component state. Promoting that
 * to a shared store would widen Task 26 beyond its stated scope, so the
 * palette command for merge is deferred — users can still trigger merge
 * via the existing button on the PR detail page.
 */

import {
  canApprovePR,
  canApproveWorkflows,
  canMarkReady,
  runApprovePR,
  runApproveWorkflows,
  runMarkReady,
  type PRDetailActionInput,
} from "../../../../../packages/ui/src/components/detail/keyboard-actions.js";
import { registerScopedActions } from "./registry.svelte.js";
import type { Action, Context } from "./types.js";

/**
 * Build a fresh PRDetailActionInput from the current keyboard Context.
 * Returns null when no PR detail is loaded (e.g. user is on a different
 * page, or capabilities/detail haven't hydrated). The action's `when`
 * returns false in that case and the handler is a no-op.
 */
export type PRDetailInputBuilder = (ctx: Context) => PRDetailActionInput | null;

/**
 * Register the PR-detail palette commands. Returns the cleanup function
 * the caller should invoke on teardown so the entries are removed and
 * hot reload doesn't accumulate duplicate registrations.
 */
export function registerPRDetailActions(getInput: PRDetailInputBuilder): () => void {
  const wrap = (
    can: (input: PRDetailActionInput) => boolean,
    run: (input: PRDetailActionInput) => void | Promise<void>,
  ) => ({
    when: (ctx: Context): boolean => {
      const input = getInput(ctx);
      return input !== null && can(input);
    },
    handler: async (ctx: Context): Promise<void> => {
      const input = getInput(ctx);
      if (input === null) return;
      await run(input);
    },
  });

  const actions: Action[] = [
    {
      id: "pr.approve",
      label: "Approve PR",
      scope: "detail-pr",
      binding: null,
      priority: 0,
      ...wrap(canApprovePR, runApprovePR),
    },
    {
      id: "pr.ready",
      label: "Mark ready for review",
      scope: "detail-pr",
      binding: null,
      priority: 0,
      ...wrap(canMarkReady, runMarkReady),
    },
    {
      id: "pr.approveWorkflows",
      label: "Approve workflows",
      scope: "detail-pr",
      binding: null,
      priority: 0,
      ...wrap(canApproveWorkflows, runApproveWorkflows),
    },
  ];

  return registerScopedActions("pr-detail-actions", actions);
}
