# Review pull requests

Review the newest pull-request context, decide whether the branch is ready,
and open local follow-up work when needed.

<figure class="workflow-shot">
  <img class="workflow-shot__image workflow-shot__image--light" src="../assets/generated/code-reviewer-light.svg" alt="kenn-forge pull-request detail in light mode">
  <img class="workflow-shot__image workflow-shot__image--dark" src="../assets/generated/code-reviewer-dark.svg" alt="kenn-forge pull-request detail in dark mode">
  <figcaption>Review status, CI, discussion, files, and workspace actions share one view.</figcaption>
</figure>

## Scan the queue

Start in **Activity** to find new comments, reviews, and state changes. Open
**Pulls** for the review queue.

The list shows review state, CI, branch information, and repository context.
Select an item to open its description, timeline, actions, and diff.

Use local workflow statuses when you need a personal queue such as "needs my
review" or "waiting for author." These statuses stay in kenn-forge and do not
change labels or project fields at the provider.

## Work the review

- Check review state and CI.
- Read the description and latest discussion.
- Inspect the diff and file tree.
- Comment, approve, mark ready, close, reopen, or merge when supported.

Unsupported actions remain visible but unavailable.

In **Files**, select one line or a contiguous range to draft an inline comment.
Drafts stay local until you publish the review. kenn-forge rejects a draft if
the pull request head changed after you started it, so comments cannot land on
the wrong revision.

Published threads appear in the pull request timeline. Thread resolution and
review actions depend on provider support.

Detected stacks show their member order. kenn-forge blocks a mid-stack merge by
default while an earlier member is still open. Land the earlier pull requests
first, or use the provider when you intentionally need a different order.

The conversation, files, and workspace can share one layout. Drag tabs to split
the view, resize the panes, or maximize the diff while reviewing a large
change.

## Move from review into a coding agent

Open **Create Workspace** and choose a configured agent such as Codex.
kenn-forge creates and tracks a Git worktree for the pull-request
branch, then launches the agent there. You do not need to run
`git worktree add` or manage a separate checkout.

<figure class="workflow-shot">
  <img class="workflow-shot__image workflow-shot__image--light" src="../assets/generated/code-reviewer-agent-launch-light.svg" alt="kenn-forge pull-request detail with the Create Workspace menu open to Codex in light mode">
  <img class="workflow-shot__image workflow-shot__image--dark" src="../assets/generated/code-reviewer-agent-launch-dark.svg" alt="kenn-forge pull-request detail with the Create Workspace menu open to Codex in dark mode">
  <figcaption>Create the review worktree and launch Codex from the pull-request view.</figcaption>
</figure>

**Workspaces** keeps the branch, session, and review links together. Return to
the pull request after local verification or follow-up changes.

<figure class="workflow-shot">
  <img class="workflow-shot__image workflow-shot__image--light" src="../assets/generated/workspace-pr-details-light.svg" alt="kenn-forge Workspaces view with a running Codex session and the linked pull request open in the right sidebar in light mode">
  <img class="workflow-shot__image workflow-shot__image--dark" src="../assets/generated/workspace-pr-details-dark.svg" alt="kenn-forge Workspaces view with a running Codex session and the linked pull request open in the right sidebar in dark mode">
  <figcaption>Keep the coding session open while checking the linked pull request in the right sidebar.</figcaption>
</figure>
