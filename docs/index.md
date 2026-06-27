# middleman user docs

middleman is a local-first maintainer console for repositories you watch every
day. It syncs pull requests, merge requests, issues, reviews, comments, CI
signals, releases, and activity into SQLite, then gives you one browser UI for
triage and action.

Use it when you already know your forge and want one place to answer: what
changed, what needs me, what can be merged, and what local work should I open
next?

## Start here

- [Quick start](quickstart.md): build, run, add repositories, and open the UI.
- [Configuration](configuration.md): repositories, provider hosts, tokens, modes,
  docs folders, messages, and telemetry.
- [Workflows](workflows.md): common ways to use Activity, PRs, issues, reviews,
  workspaces, Kata, Docs, Messages, and fleet views.
- [Commands](commands.md): CLI commands for serving, status, docs folders, and
  GitHub App credentials.
- [Troubleshooting](troubleshooting.md): startup, auth, sync, config, database,
  and mode issues.

## What middleman can do

- Show a cross-repository Activity feed with comment, review, commit, PR, and
  issue activity.
- Browse PRs/MRs and issues with filters, keyboard navigation, details, comments,
  review actions, state changes, and merge actions where the provider supports
  them.
- Move with the command palette and view-specific keyboard shortcuts.
- Inspect diffs, changed files, CI status, branch metadata, review state, labels,
  and release signals without leaving the console.
- Track PRs on a local kanban board. Board state stays in middleman and does not
  mutate provider labels or projects.
- Launch and attach to local workspace sessions for repository work.
- Browse repository source, branches, and files from the UI.
- Use optional modes for Kata task daemons, local markdown docs, and msgvault
  message search.
- Federate middleman daemons so one machine can view and act on items owned by
  another machine.
- Run as one local daemon with an embedded web app, local SQLite storage, and a
  single TOML config file.

## What middleman is not

- It is not a hosted service.
- It is not a replacement data source for your forge, Kata, local docs, or
  msgvault. Those systems remain the source of truth.
- It is not a general multi-user server by default. It binds to loopback unless
  you configure otherwise.
