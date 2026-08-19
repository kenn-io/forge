# Triage issues

Find the issue that needs attention now. Recent activity matters more than
arrival order.

<figure class="workflow-shot">
  <img class="workflow-shot__image workflow-shot__image--light" src="../assets/generated/issue-triager-light.svg" alt="kenn-forge issue detail in light mode">
  <img class="workflow-shot__image workflow-shot__image--dark" src="../assets/generated/issue-triager-dark.svg" alt="kenn-forge issue detail in dark mode">
  <figcaption>Review issue context, state, labels, and workspace actions in one view.</figcaption>
</figure>

## Scan the queue

Start in **Activity**. Filter to issues, then hide closed items or bot activity
when they add noise.

Open **Issues** for a focused list. Select an issue to read its title, body,
labels, assignees, comments, and state.

Search by text or issue number. Narrow the list by repository, state, whether
you are involved, stars, or bot authorship. A star is private to kenn-forge and
works well as a short personal follow-up list.

## Decide the next action

- Read the newest activity first.
- Check labels and assignees, and change them when the provider supports it.
- Comment when you need more information.
- Close or reopen when the provider and credential allow it.
- Star work that needs another pass.

## Start implementation

Choose **Create Workspace** from the issue detail. kenn-forge creates a local
worktree and opens **Workspaces**. Start a shell or configured agent on that
branch.

If the expected issue worktree already exists, the create flow can recover it
after checking that it belongs to the same managed repository. kenn-forge does
not adopt a directory from another repository or branch.

When a linked Kata issue exists, open the **Kata** tab for read-only task detail
or to create the mapped Kata workspace.
