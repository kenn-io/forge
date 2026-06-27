# I am a code reviewer

Use middleman to review the newest PR context, check whether the branch is
ready, and move into local follow-up work when needed.

<figure class="workflow-shot">
  <img class="workflow-shot__image workflow-shot__image--light" src="../assets/generated/code-reviewer-light.svg" alt="middleman code review view in light mode">
  <img class="workflow-shot__image workflow-shot__image--dark" src="../assets/generated/code-reviewer-dark.svg" alt="middleman code review view in dark mode">
  <figcaption>PR detail brings together review status, CI context, discussion, files, and workspace creation.</figcaption>
</figure>

## Review from newest context

Start in **Activity**. The newest PR comments, reviews, and state changes appear
first, so changed reviews surface quickly.

Switch to **Pulls** for the review queue. The list keeps PR state, review
decision, CI signal, branch information, and repository context visible. The
detail pane holds the description, timeline, actions, and diff entry points.

## Work the review

For each PR:

- Open the most recently active item first.
- Check review state and CI before spending time in the diff.
- Read the description and latest discussion.
- Use the diff and file tree for line-by-line review.
- Comment, approve, mark ready, close, reopen, or merge when the provider and
  token support that action.

Unsupported provider actions are shown as unavailable instead of pretending every
forge behaves like GitHub.

## Drop into a workspace

When a review needs local verification or follow-up changes, use **Create
Workspace** from the PR detail pane. middleman creates a worktree for the PR head
and opens the workspace surface. You can run a shell or configured agent without
finding the repo, branch, or clone path by hand.

Read the newest context, inspect the branch, open a workspace, then return to
the PR action surface.
