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

Use the sidebar to switch between modes. Use the command palette when you know
the action or destination but do not want to hunt through the page. Press `?` to
see the shortcuts available in the current view.

The UI supports repeated keyboard triage. List movement, detail panes, drawer
closing, mode switching, and search stay inside the console flow.

## Review and merge

Open **Pulls** to work through PRs and MRs.

From the detail view you can:

- Read the description and discussion.
- Inspect changed files and inline diffs.
- Check CI and branch status.
- Comment.
- Approve where supported.
- Mark drafts ready where supported.
- Close, reopen, or merge where supported.
- Star items for quick follow-up.

Provider-specific differences are shown as disabled or unavailable actions
rather than hidden GitHub-only behavior.

## Track local PR state

Open **Board** when you want a local maintainer queue. Drag PRs through the
columns that match your process. Board state is stored in middleman and does not
write provider labels, milestones, projects, or fields.

## Work issues

Open **Issues** to search, filter, comment, close, reopen, and star issues.

When workspaces are configured, an issue can become a local work session. You
can move from triage to implementation without hunting for the repository.

## Inspect repository source

Use **Repos** to browse configured repositories, branches, and files. This is
useful when a review or issue references code and you need quick context without
opening the forge.

## Work in local sessions

Use **Workspaces** to launch and attach to shell or agent sessions tied to local
repositories. tmux-backed sessions let middleman keep a durable attach point for
ongoing work.

## Use Kata tasks

Enable Kata mode when your work is tracked in Kata. middleman discovers Kata
daemons from Kata's own config and runtime records. You can browse tasks, open
details, update task state, and cross-link task references from Docs or Messages
when the source contains them.

Kata task data stays in Kata; middleman is the console.

## Browse and edit docs

Enable Docs mode and register markdown folders. Use it to browse, search, read,
edit, and publish local docs from the same console you use for code review.

Docs files stay on disk. middleman only operates inside the configured folders.

## Search messages

Enable Messages mode when msgvault is available. Use it to search messages,
inspect details, follow threads, and open linked Kata items where possible.

Messages stay in msgvault. middleman proxies and renders them safely.

## Use a fleet

Fleet mode lets one middleman daemon view snapshots from other middleman
daemons. The hub can route supported mutations back to the machine that owns the
resource and can expose attach commands for remote sessions.

Use HTTP peers for reachable daemons or SSH peers when the remote listener
should stay private. See [Federated fleet](federated-fleet.md) for the full
fleet shape.
