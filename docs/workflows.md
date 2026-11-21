# Workflows

## Scan recent activity

Open Kenn Forge's **Activity** view to see your daily queue across tracked
repositories. You can filter by time, event type, repository, item type, text,
closed state, or bot activity.

Threaded mode groups events by pull request or issue, while flat mode keeps
exact event order. Select any row to open its detail without leaving the queue.

When one repository needs fresh data, choose **Sync current repo** to update it
without scheduling a global refresh.

See [Follow activity across repositories](workflows/activity.md) for saved
filter URLs, local workspace activity, commit diffs, and detail panes.

## Follow a role-based guide

- [Triage an issue](workflows/issue-triager.md)
- [Review a pull request](workflows/code-reviewer.md)

## Move quickly

Use the sidebar to switch modes and the repository selector to narrow your
scope. To open the command palette, press `Cmd/Ctrl+K` or `Cmd/Ctrl+P`. When a
terminal has focus, press `Cmd/Ctrl+Shift+K` instead.

Add a prefix in the palette to narrow results:

- `>` for commands
- `pr:` for pull requests
- `issue:` for issues

To find pull requests and issues by topic, such as "flaky CI" or "login
redirect loops", switch the palette from **Title** to **Meaning**. Forge ranks
titles, descriptions, and comments across open and closed items. Meaning
requires [semantic search](configuration.md#semantic-search) to be configured.

Press `?` to see keyboard shortcuts for the current view.

## Review and merge

Open **PRs** and select a pull request. Its detail view brings together the
description, discussion, CI status, branch state, review state, changed files,
and provider actions.

In the PRs and Issues sidebars, **Closed** and **All** load the 200 most
recently active items. More items load as you scroll, with **Retry** if a
request fails, and browser-only filters apply to the loaded rows.

Use the **View** menu to filter the timeline or compact rows, and open the file
tree for line-by-line review. You can comment, approve, mark ready, close,
reopen, or merge when the provider and your credential allow it. Actions a
provider does not support remain visible but unavailable.

To merge several pull requests at once, Shift- or Cmd/Ctrl-click them in the
list, right-click, and choose **Merge**. Ready pull requests merge right away,
and those with pending CI merge once their checks pass. Drafts, conflicts, and
failed CI are skipped.

<figure class="workflow-shot">
  <img class="workflow-shot__image workflow-shot__image--light" src="assets/generated/pull-list-bulk-actions-light.svg" alt="Forge pull request list with three selected pull requests and the action menu open in light mode">
  <img class="workflow-shot__image workflow-shot__image--dark" src="assets/generated/pull-list-bulk-actions-dark.svg" alt="Forge pull request list with three selected pull requests and the action menu open in dark mode">
  <figcaption>Right-click a selection to merge, label, assign, or run a quick action on every selected pull request.</figcaption>
</figure>

Multi-select works in both the pull request and issue lists. Right-click a
selection to copy links, star, edit labels or assignees, close, reopen, or run
a quick action on each item. Pull requests also offer **Mark ready for review**
and **Convert to draft**. Label and assignee edits touch only the values you
change.

Detected stacks show their member order, and mid-stack merges stay blocked by
default until earlier members land.

On GitHub.com repositories whose default branch uses a merge queue, **Add to
merge queue** replaces the merge action, and both pull rows and the detail view
show queue position and state. If the queue removes a pull request without
merging it, the detail view keeps the reason until you queue it again.

**Queues**, next to **PRs**, lists every merge queue in one place. For each
repository and branch, it shows queued pull requests in order with their state
and estimated merge time, along with pull requests removed by the queue and the
checks that failed. You can search by title, number, author, repository, or
branch, and filter by status, author, or the header repository filter. Select a
pull request to open it beside the queues, with its workspace one click away.
**Delete workspace after merge** waits for the queue, deleting the workspace
once the pull request merges and keeping it if the pull request closes or
leaves the queue unmerged.

Conversation, files, and workspace panes can share one layout. Reorder, split,
resize, hide, or maximize panes as your task changes.

## Work through a worklist

Ask an agent connected through the [MCP companion](kenn-forge-mcp.md) to build
a ranked list of pull requests and issues. Open **Worklists** to step through
the items, see what changed since the agent wrote the list, and mark items
handled. See [Work through a worklist](workflows/worklists.md).

## Run manual provider workflows

**Actions** is opt-in. To turn it on, open **Settings → Visible modes**, enable
**Actions**, and save.

1. Open **Actions** and select a repository.
2. Pick a manual workflow and a Git ref.
3. Fill in any typed inputs and run the workflow.

Recent runs show their status, jobs, and steps. Older runs load as you scroll,
and retryable failures offer **Retry**. Follow the provider link on a run to
inspect its full logs. You can also start available workflows from a pull
request's **Actions** menu. Open same-repository pull requests start from the
head branch, whereas fork and merged pull requests start from the target
branch.

## Work with issues

Open **Issues** to search, filter, comment, star, close, or reopen issues. When
an issue is ready for implementation, create a workspace directly from it.

## Browse repository source

Open **Repos** and pick a repository card, or run **View repository source**
from the command palette. Switch between branches and tags, filter the path
tree, and read files as source. Markdown files also have a rendered preview,
and the history rail lists commits for the selected file.

The URL records the repository, ref, path, and preview mode, so copied links
and browser back/forward navigation restore the same view. Provider links take
you back to the original repository when you need it.

## Work in local sessions

Create a workspace from a pull request, issue, Kata task, or **New workspace**.
Each workspace creates a Git worktree. To create a workspace and launch an
agent in one step, pick the agent from the create menu.

New workspaces can start from any tracked repository. Pick a branch name or let
Forge create one from the default branch.

Workspaces use tmux-backed sessions for durable attachment. You can launch more
shells or agents from the workspace header. Promote a session into the detail
layout when you want it beside discussion or files.

To show agent activity in workspace rows, install the lifecycle hooks:

```sh
kenn-forge agent-hook install
```

Add `--agent NAME` to install hooks for a single agent. Active work, approval
requests, and input requests update while the sidebar is open. Hook reports
stay active until the agent session ends or its terminal runtime is removed,
after which terminal-based activity detection resumes.

See [Work in local sessions](workflows/workspaces.md) for workspace types,
session layouts, tmux attachment, phone use, deletion, and recovery.

## Use Kata tasks

Link a Kata issue from a pull request, provider issue, or workspace to view its
read-only detail inline. To search a selected daemon and create or reopen a mapped
workspace, use **New workspace → Kata issue**. If Forge cannot match the Kata
project to a configured repository, open **Settings → Kata mappings** and add
an override. Open the task in Kata when you need to edit it.

Kata remains the source of truth for task data. Forge owns only the links to
pull requests, issues, and workspaces, and there is no separate Kata mode.

## Review Roborev jobs

Start the Roborev daemon, then open **Reviews** in a pull request's workspace.
Jobs load for that workspace's repository and branch when you open the panel.
Select a job to read the review, inspect its log and prompt, add a comment, or
run the actions available for its current state.

See [Integrations](integrations.md#review-roborev-jobs) for endpoint setup and
a walkthrough of the Reviews panel.

## Browse and edit Docs

Enable **Docs** mode and register Markdown folders to browse, search, read,
edit, pull, and publish files from the console. Task references can open a Kata
task through the folder's daemon binding.

Files stay on disk inside the configured folders.

See [Read and edit local Docs](workflows/docs.md) for search, file editing, Git
pull and publish, and Kata reference behavior.

## Use a fleet

A hub combines snapshots from enrolled Forge spokes. Its workspace list shows
which node owns each workspace, and **New workspace** can create repository
work on the hub or a writable spoke. A spoke keeps workspace creation and its
workspace list local.

Supported actions route back to the machine that owns the resource, and
terminal traffic uses the spoke's WebSocket API.

Each daemon must have a reachable HTTPS origin. Forge does not use SSH for
fleet transport or daemon startup. See [Federated fleet](federated-fleet.md).
