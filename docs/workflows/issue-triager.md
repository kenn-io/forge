# Triage issues

Use Kenn Forge to find the issue that needs attention now. Recent activity
matters more than arrival order.

<figure class="workflow-shot">
  <img class="workflow-shot__image workflow-shot__image--light" src="../assets/generated/issue-triager-light.svg" alt="Forge issue detail in light mode">
  <img class="workflow-shot__image workflow-shot__image--dark" src="../assets/generated/issue-triager-dark.svg" alt="Forge issue detail in dark mode">
  <figcaption>Review issue context, state, labels, and workspace actions in one view.</figcaption>
</figure>

## Scan the queue

Start in **Activity**, filter to issues, and hide closed items or bot activity
when they add noise.

For a focused list, open **Issues** and select an issue to read its title,
body, labels, assignees, comments, and state.

Search by text or issue number, and narrow the list by repository, state,
whether you are involved, stars, or bot authorship. Stars are private to Forge
and work well as a short personal follow-up list.

## Decide the next action

- Read the newest activity first.
- Check labels and assignees, and update them when the provider supports it.
- Comment when you need more information.
- Close or reopen the issue when the provider and your credential allow it.
- Star work that needs another pass.

## Start implementation

When an issue is ready for code, select **Create Workspace** in the issue
detail. Forge creates a local worktree and opens **Workspaces**, where you can
start a shell or configured agent on that branch.

If the expected issue worktree already exists, Forge can recover it after
verifying that it belongs to the same managed repository. Forge does not adopt
a directory from another repository or branch.

When an issue has a linked Kata task, open the **Kata** tab to view read-only
task detail or create the mapped Kata workspace.
