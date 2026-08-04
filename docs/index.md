# kenn-forge

kenn-forge gives maintainers one local console for repository activity, pull
requests, issues, reviews, and working sessions. It syncs provider data into
SQLite and serves the UI from a single binary.

<div class="docs-actions">
  <a class="md-button md-button--primary" data-download-current href="https://github.com/kenn-io/forge/releases">Download latest release</a>
  <a class="md-button" href="quickstart/">Quick Start</a>
</div>

<p class="docs-download-meta"><span data-download-version></span>Linux, macOS, and Windows</p>

Use kenn-forge to answer four daily questions:

- What changed across the repositories I maintain?
- Which pull requests and issues need attention?
- What can I review, update, or merge now?
- Which branch or task should I open locally?

<figure class="workflow-shot">
  <img class="workflow-shot__image workflow-shot__image--light" src="assets/generated/maintainer-overview-light.svg" alt="kenn-forge Activity with a selected pull request, live workspace, and coding-agent session in light mode">
  <img class="workflow-shot__image workflow-shot__image--dark" src="assets/generated/maintainer-overview-dark.svg" alt="kenn-forge Activity with a selected pull request, live workspace, and coding-agent session in dark mode">
  <figcaption>Activity keeps the selected pull request beside its live workspace and coding-agent session.</figcaption>
</figure>

## Start here

New users should follow the [Quick Start](quickstart.md). It covers
installation, provider setup, repository selection, the first sync, and
workspace creation.

Returning users can jump to a task:

- [Triage an issue](workflows/issue-triager.md)
- [Review a pull request](workflows/code-reviewer.md)
- [Run daily maintainer workflows](workflows.md)
- [Change repositories, tokens, or modes](configuration.md)
- [Fix startup, authentication, or sync problems](troubleshooting.md)

## Main views

**Activity** collects recent comments, reviews, commits, and state changes.
Filter by time, repository, item type, event type, or text.

**Pulls** and **Issues** combine lists, details, discussion, provider actions,
and workspace creation. Unsupported provider actions remain visible but
unavailable.

**Workspaces** opens local shells and configured agents against repository
worktrees. **Repos** browses configured source. Optional **Kata** and **Docs**
modes connect external task daemons and local Markdown folders.

## Advanced use

- [Command reference](commands.md)
- [Historical activity archives](archive.md)
- [Federated fleets](federated-fleet.md)

kenn-forge runs on your machine. Your provider, Kata daemons, and local files
remain the source of truth.
