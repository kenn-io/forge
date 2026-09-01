# Work in local sessions

A workspace ties a tracked repository and Git worktree to the shell and agent
sessions doing the work. Create one from a pull request, provider issue, linked
Kata issue, or the **New workspace** button.

<figure class="workflow-shot">
  <img class="workflow-shot__image workflow-shot__image--light" src="../assets/generated/workspace-codex-session-light.svg" alt="Forge Workspaces with a pull request worktree and running Codex session in light mode">
  <img class="workflow-shot__image workflow-shot__image--dark" src="../assets/generated/workspace-codex-session-dark.svg" alt="Forge Workspaces with a pull request worktree and running Codex session in dark mode">
  <figcaption>A workspace keeps its branch, linked item, files, diff, and running sessions together.</figcaption>
</figure>

## Create the right workspace

From a pull request, Kenn Forge checks out the pull request branch. From an
issue, it starts from the repository's default branch and creates an issue
branch. An ad-hoc workspace starts from any tracked repository and uses the
branch name you choose.

The **New workspace** dialog also searches a selected Kata daemon. A Kata task
needs a repository mapping before Forge can create its worktree.

Choose an agent from the create menu when you want to create the workspace and
launch that agent in one step. Creating the same item or ad-hoc branch again
reopens the existing workspace instead of making a duplicate.

### Choose a machine in a fleet

On a fleet hub, **New workspace** includes a **Run on** selector. Choose the hub
or any spoke that currently allows workspace writes. An unavailable machine
stays in the list with the reason it cannot be selected. The chosen machine
owns the worktree, Git commands, agent processes, and tmux sessions.

The hub workspace list adds a node badge to every row, so local and remote
workspaces are easy to tell apart. A spoke shows only its local workspaces and
creates new workspaces on itself.

The workspace buttons on pull requests and issues create work on the Forge UI
you opened. Use the hub's **New workspace** dialog when you need to choose a
different execution machine. See [Federated fleet](../federated-fleet.md) for
the complete ownership model and setup workflow.

## Use the workspace view

The workspace sidebar can search, sort, and group workspaces. Rows show the
repository, branch, linked item, changed-line counts, and current agent state
when lifecycle hooks report one.

The selected workspace includes:

- **Workflow** for the base shell and launched agent or shell sessions
- **Details** for the linked pull request, issue, or Kata task
- **Files** for the worktree file list and file preview
- **Diff** for uncommitted and branch changes

Launch another shell or agent from the workspace header. Sessions continue
under tmux when you leave the page, reload the browser, or attach from another
terminal.

## Put sessions beside the work

Pull request, issue, and Activity detail can host the same live workspace. Move
a session into its own pane when you want the terminal beside the conversation
or diff. You can reorder tabs, drag a tab to split the layout, resize panes,
hide a pane, or maximize the one that needs the screen.

Closing a pane changes the layout. It does not stop the underlying tmux
session. Use the session controls when you intend to stop a process.

## Show agent state in the sidebar

Install lifecycle hooks for the agents Forge supports:

```sh
kenn-forge agent-hook install
```

Use `--agent NAME` to install one agent's hook. The sidebar can then show when
an agent is working, waiting for approval, waiting for input, or done. Without
fresh hook reports, Forge falls back to tmux activity.

## Attach outside the browser

Workspace and session controls expose tmux attach commands. This is useful for
a full local terminal or for a remote workspace reached through Fleet. The
browser and terminal attach to the same durable session.

## Delete or recover a workspace

Delete removes the kenn-forge-managed worktree and its workspace record. A
dirty worktree blocks normal deletion so uncommitted work is not discarded.
Review the files first. Force deletion is available only after that warning.

If issue workspace creation finds the expected directory already present,
Forge can recover it after verifying that it belongs to the managed
repository. A mismatched repository or branch stays untouched and needs manual
attention.

## Use workspaces on a phone

Open `/m/workspaces` for the phone layout. It uses workspace cards and shows one
terminal at a time. The session picker moves between the base shell and running
agents without stopping the sessions in the background.

<figure class="workflow-shot workflow-shot--phone">
  <img class="workflow-shot__image workflow-shot__image--light" src="../assets/generated/mobile-workspace-session-light.svg" alt="Forge phone workspace with a Codex terminal and the input composer open in light mode">
  <img class="workflow-shot__image workflow-shot__image--dark" src="../assets/generated/mobile-workspace-session-dark.svg" alt="Forge phone workspace with a Codex terminal and the input composer open in dark mode">
  <figcaption>The phone view keeps one terminal on screen, with session switching at the top and software-keyboard input at the bottom.</figcaption>
</figure>

The phone composer helps with software keyboards, while direct terminal input
still works with a hardware keyboard. Pull request and issue links return to the
same mobile workspace and selected session.
