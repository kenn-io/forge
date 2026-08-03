# Workflows

## Scan recent activity

Open **Activity** for the daily queue. Filter by time, event type, repository,
item type, text, closed state, or bot activity.

Threaded mode groups events by pull request or issue. Flat mode keeps exact
event order. Select a row to open its detail without leaving the queue.

Use **Sync current repository** when one repository needs fresh data. This
avoids scheduling a global refresh.

## Follow a role-based guide

- [Triage an issue](workflows/issue-triager.md)
- [Review a pull request](workflows/code-reviewer.md)

## Move quickly

Use the sidebar for modes and the repository selector for scope. Open the
command palette with `Cmd/Ctrl+K` or `Cmd/Ctrl+P`. Use `Cmd/Ctrl+Shift+K`
while a terminal has focus.

Palette prefixes narrow results:

- `>` for commands
- `pr:` for pull requests
- `issue:` for issues

Press `?` for shortcuts in the current view.

## Review and merge

Open **Pulls**, then select an item. The detail view combines description,
discussion, CI, branch state, review state, changed files, and provider
actions.

Use the **View** menu to filter the timeline or compact rows. Open the file
tree for line-by-line review. Comment, approve, mark ready, close, reopen, or
merge when the provider and credential allow it.

Unsupported actions remain visible but unavailable.

Detected stacks show member order. Mid-stack merges stay blocked by default
until earlier members land.

The conversation, files, and workspace can share a pane layout. Reorder,
split, resize, hide, or maximize panes as the task changes.

## Track local pull-request state

Set a workflow status from the pull-request detail and filter the list by one
or more statuses. This status stays in Kenn Forge. It does not change provider
labels, milestones, projects, or fields.

## Work with issues

Open **Issues** to search, filter, comment, star, close, or reopen issues.
Create a workspace when an issue is ready for implementation.

## Browse repository source

Open **Repos** to inspect repository summaries, switch branches, search paths,
and read source files. Provider links return to the original item when needed.

## Work in local sessions

Create a workspace from a pull request, issue, Kata task, or the **New
workspace** action. A workspace creates a worktree. Choose an agent from the
action menu to create and launch in one step.

New workspaces can start from any tracked repository. Choose a branch name or
let Kenn Forge create one from the default branch.

Workspaces use tmux-backed sessions for durable attachment. Launch more shells
or agents from the workspace header. Promote a session into the detail layout
when you want it beside discussion or files.

Install lifecycle hooks to show agent activity in workspace rows:

```sh
kenn-forge agent-hook install
```

Use `--agent NAME` to limit installation. Active work, approval requests, and
input requests update while the sidebar is open. Hook reports expire after 30
minutes without another event, then fall back to tmux activity.

## Use Kata tasks

Enable Kata mode to work with daemons listed in Kata's own config and runtime
records. Filter tasks by project, status, text, columns, and links. Open the
dependency graph or create a workspace when a task resolves to one registered
repository.

Kata remains the source of truth for task data.

## Browse and edit Docs

Enable Docs mode and register Markdown folders. Browse, search, read, edit,
pull, and publish files from the console. Task references can open a Kata task
through the folder's daemon binding.

Files remain on disk inside the configured folders.

## Use a fleet

A hub can combine snapshots from other Kenn Forge daemons. Supported actions
route back to the machine that owns the resource. Sessions expose local or
remote attach commands.

Use HTTP on a trusted private network. Use SSH when the peer should not expose
its listener. See [Federated fleet](federated-fleet.md).
