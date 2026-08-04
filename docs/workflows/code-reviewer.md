# I am a code reviewer

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

## Work the review

- Check review state and CI.
- Read the description and latest discussion.
- Inspect the diff and file tree.
- Comment, approve, mark ready, close, reopen, or merge when supported.

Unsupported actions remain visible but unavailable.

## Move from review into a coding agent

Open **Create Workspace** and choose a configured agent such as Codex. Kenn
Forge automatically creates and tracks a Git worktree for the pull-request
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
  <img class="workflow-shot__image workflow-shot__image--light" src="../assets/generated/workspace-codex-session-light.svg" alt="kenn-forge Workspaces view with a pull-request worktree and running Codex session in light mode">
  <img class="workflow-shot__image workflow-shot__image--dark" src="../assets/generated/workspace-codex-session-dark.svg" alt="kenn-forge Workspaces view with a pull-request worktree and running Codex session in dark mode">
  <figcaption>Workspaces tracks the pull-request branch and its running Codex session.</figcaption>
</figure>
