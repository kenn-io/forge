# Workflows

## Daily triage

Open **Activity** first. Use it to scan comments, reviews, commits, PRs, and
issues across your configured repositories.

Useful filters:

- Time range: 24h, 7d, 30d, or 90d.
- Event type: comments, reviews, commits, and state changes.
- Repository and item type.
- Hide closed items.
- Hide bot activity.
- Free-text search.

Threaded mode groups events by PR or issue. Flat mode is better when exact event
order matters.

## Role-based walkthroughs

- [I am an issue triager](workflows/issue-triager.md): start with the newest
  issue activity, decide what needs attention, and create a workspace when work
  is ready.
- [I am a code reviewer](workflows/code-reviewer.md): start with the newest PR
  review context, check CI and discussion, and open a local worktree when review
  needs hands-on verification.

## Move around the UI

Use the sidebar to switch between modes. Open the command palette with
`Cmd/Ctrl+K` or `Cmd/Ctrl+P` when you know the action or destination but do not
want to hunt through the page. Use `Cmd/Ctrl+Shift+K` while a terminal has
focus. Prefix a search with `>` for commands, `pr:` for pull requests, or
`issue:` for issues. Press `?` to see the shortcuts available in the current
view.

The UI supports repeated keyboard triage. List movement, detail panes, drawer
closing, mode switching, and search stay inside the console flow.

The repository selector filters every provider-qualified repository in the
current mode. Use **Sync current repository** when you need fresh data for the
selected item without scheduling a global refresh.

## Review and merge

Open **Pulls** to work through PRs and MRs.

From the detail view you can:

- Read the description and discussion.
- Inspect changed files and inline diffs.
- Use the shared **View** menu to filter the timeline or switch compact rows,
  and copy provider links for individual comments.
- Edit or delete your own comments where the provider supports it.
- Check CI and branch status.
- Comment.
- Approve where supported.
- Mark drafts ready where supported.
- Close, reopen, or merge where supported.
- Star items for quick follow-up.

Provider-specific differences are shown as disabled or unavailable actions
rather than hidden GitHub-only behavior.

When kenn-forge detects a stacked PR series, the detail view shows its members
and ordering. GitHub native stack data can improve detection when enabled, but
the same UI and branch-based fallback remain authoritative. Merging a middle
member is blocked by default until earlier members land.

Conversation, files, and an open workspace share a rearrangeable pane layout.
Drag tabs to reorder or split them, resize the resulting panes, hide or maximize
panes, and reopen them from the tab strip or command palette. Long PR and issue
descriptions start expanded and can be collapsed when they crowd the working
area.

## Track local PR state

Set a PR's workflow status from its detail view, and filter the PR list by one
or more statuses. Workflow status is stored in kenn-forge and does not write
provider labels, milestones, projects, or fields.

## Work issues

Open **Issues** to search, filter, comment, close, reopen, and star issues.

When workspaces are configured, an issue can become a local work session. You
can move from triage to implementation without hunting for the repository.

## Inspect repository source

Use **Repos** to browse configured repositories, inspect summary metadata,
switch branches, search paths, open source files, and follow references back to
provider items. This is useful when a review or issue references code and you
need quick context without opening the forge.

## Work in local sessions

Use **Workspaces** to launch and attach to shell or agent sessions tied to local
repositories. tmux-backed sessions let kenn-forge keep a durable attach point for
ongoing work. The primary **Create Workspace** action creates only; choose an
agent from its dropdown to create and launch immediately without another modal.
The choice is per-creation, session-scoped intent, not a persistent default.

Once a workspace is ready, launch another session from its header. A session can
stay in the workspace controls or be promoted into the detail pane layout and
moved beside the conversation or files without opening a second terminal
connection.

New work does not need an existing pull request, issue, or Kata task. Use **New
workspace** in the Workspaces sidebar, or the same command in the palette
(Cmd/Ctrl+K), to pick a tracked repository and start a fresh worktree. The
picker preselects the repository you last started work in. Name the
branch or leave it empty and kenn-forge generates one; either way the worktree
branches from the repository's default branch.

Run or rerun `kenn-forge agent-hook install` to show activity from Claude Code,
Codex, GitHub Copilot CLI, Cursor, Factory Droid, Gemini CLI, Hermes Agent, and
Qwen Code in workspace rows. The command installs all supported integrations by
default; pass `--agent NAME` to install only one. The rows distinguish active
work, approval requests, and user input, refreshing within five seconds while
the sidebar is open. Reports expire after 30 minutes without another hook event
and then fall back to tmux activity. Installed hooks forward lifecycle events
to the running kenn-forge daemon. Claude sessions also receive a workspace
summary regenerated from persisted workspace metadata at session start;
`CLAUDE.local.md` is never read for it. Codex asks you to review the installed
command through `/hooks` once.

## Use Kata tasks

Enable Kata mode when your work is tracked in Kata. kenn-forge discovers Kata
daemons from Kata's own config and runtime records. You can browse tasks, open
details, update task state, and cross-link task references from Docs when the
source contains them.

Use project scope, the **Open**, **Ready**, **Closed**, and **All** status
filters, text search, optional columns, and link filters to narrow the task
tree. Parent and child tasks stay together while you expand the hierarchy. Open
the reachable-task graph when dependencies matter, or create a workspace when a
task resolves to exactly one registered repository. Configure project-to-repo
mappings when Kata cannot infer that repository unambiguously. Kata task data
stays in Kata; kenn-forge is the console.

## Browse and edit docs

Enable Docs mode and register markdown folders. Use it to browse, search, read,
edit, pull, and publish local docs from the same console you use for code review.
Folder trees show markdown changes, and task references can open the matching
Kata task using the folder's configured daemon binding.

Docs files stay on disk. kenn-forge only operates inside the configured folders.

## Use a fleet

Fleet mode lets one kenn-forge daemon view snapshots from other kenn-forge
daemons. The hub can route supported mutations back to the machine that owns the
resource and can expose attach commands for remote sessions.

Use HTTP peers for reachable daemons or SSH peers when the remote listener
should stay private. See [Federated fleet](federated-fleet.md) for the full
fleet shape.
