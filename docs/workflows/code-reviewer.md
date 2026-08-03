# I am a code reviewer

Review the newest pull-request context, decide whether the branch is ready,
and open local follow-up work when needed.

<figure class="workflow-shot">
  <img class="workflow-shot__image workflow-shot__image--light" src="../assets/generated/code-reviewer-light.svg" alt="Kenn Forge pull-request detail in light mode">
  <img class="workflow-shot__image workflow-shot__image--dark" src="../assets/generated/code-reviewer-dark.svg" alt="Kenn Forge pull-request detail in dark mode">
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

## Verify locally

Choose **Create Workspace** when the review needs local verification or
follow-up changes. Kenn Forge creates a worktree for the pull-request head.
Run a shell or configured agent, then return to the review.
