# Review pull requests

Use Kenn Forge to review the newest pull request context, decide whether the
branch is ready, and open local follow-up work when you need it.

<figure class="workflow-shot">
  <img class="workflow-shot__image workflow-shot__image--light" src="../assets/generated/code-reviewer-light.svg" alt="Forge pull-request detail in light mode">
  <img class="workflow-shot__image workflow-shot__image--dark" src="../assets/generated/code-reviewer-dark.svg" alt="Forge pull-request detail in dark mode">
  <figcaption>Review status, CI, discussion, files, and workspace actions share one view.</figcaption>
</figure>

## Scan the queue

Start in **Activity** to spot new comments, reviews, and state changes. Open
**PRs** for the review queue.

Each row shows review state, CI, branch information, and repository context.
Select a pull request to open its description, timeline, actions, and diff.

To filter by CI status, choose **Passing CI** for pull requests whose checks
all passed with none failed or still running, or **Failed CI** for pull
requests with a failed check. A pull request with no reported CI matches
neither.

## Work the review

- Check review state and CI.
- Read the description and latest discussion.
- Inspect the diff and file tree.
- Comment, approve, mark ready, close, reopen, or merge when supported.

Actions that a provider does not support remain visible but unavailable.

To approve without leaving a comment, double-click **Approve**.

Videos attached to descriptions, comments, and review comments play inline
where the provider plays them. For private repositories, Forge loads them with
your provider credential. To watch a video larger without leaving the page,
select the expand button in the video's controls, next to full screen. Playback
continues in the larger view and picks up inline when you close it. The speed
button sets playback speed, and Forge remembers your choice for the next videos
you open.

In **Files**, select a line to draft an inline comment. When the provider
supports native multiline ranges, you can also select a contiguous range.
Drafts stay local until you publish the review. If the pull request head
changes after you start a draft, Forge rejects the draft so comments cannot
land on the wrong revision.

Published threads appear in the pull request timeline. Thread resolution and
review actions depend on provider support.

Detected stacks show their member order, and Forge blocks mid-stack merges by
default while an earlier member is still open. Land the earlier pull requests
first, or use the provider when you intentionally need a different order.

Conversation, files, and workspace panes can share one layout. Drag tabs to
split the view, resize panes, or maximize the diff when reviewing a large
change.

## Move from review into a coding agent

Open **Create Workspace** and pick a configured agent, such as Codex. Forge
creates and tracks a Git worktree for the pull request branch and launches the
agent there. You do not need to run `git worktree add` or manage a separate
checkout.

<figure class="workflow-shot">
  <img class="workflow-shot__image workflow-shot__image--light" src="../assets/generated/code-reviewer-agent-launch-light.svg" alt="Forge pull-request detail with the Create Workspace menu open to Codex in light mode">
  <img class="workflow-shot__image workflow-shot__image--dark" src="../assets/generated/code-reviewer-agent-launch-dark.svg" alt="Forge pull-request detail with the Create Workspace menu open to Codex in dark mode">
  <figcaption>Create the review worktree and launch Codex from the pull-request view.</figcaption>
</figure>

**Workspaces** keeps the branch, session, and review links together, and you
can return to the pull request after local verification or follow-up changes.

<figure class="workflow-shot">
  <img class="workflow-shot__image workflow-shot__image--light" src="../assets/generated/workspace-pr-details-light.svg" alt="Forge Workspaces view with a running Codex session and the linked pull request open in the right sidebar in light mode">
  <img class="workflow-shot__image workflow-shot__image--dark" src="../assets/generated/workspace-pr-details-dark.svg" alt="Forge Workspaces view with a running Codex session and the linked pull request open in the right sidebar in dark mode">
  <figcaption>Keep the coding session open while checking the linked pull request in the right sidebar.</figcaption>
</figure>
