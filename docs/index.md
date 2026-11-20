---
title: Overview
---

# Kenn Forge

Forge gives you one local console for repository activity, pull requests,
issues, reviews, and coding-agent sessions. It syncs provider data into SQLite
and serves the UI from a single binary.

For a short visual tour, start with the [Guide to Forge](/guide/).

<div class="docs-actions">
  <a class="md-button md-button--primary" data-download-current href="https://github.com/kenn-io/forge/releases">Download latest release</a>
  <a class="md-button" href="quickstart/">Quick start</a>
</div>

<p class="docs-download-meta"><span data-download-version></span>Linux, macOS, and Windows</p>

Forge helps you answer four daily questions:

- What changed across the repositories I maintain?
- Which pull requests and issues need attention?
- What can I review, update, or merge now?
- Which branch or task should I open locally?

<figure class="workflow-shot">
  <img class="workflow-shot__image workflow-shot__image--light" src="assets/generated/maintainer-overview-light.svg" alt="Forge Activity with a selected pull request, live workspace, and coding-agent session in light mode">
  <img class="workflow-shot__image workflow-shot__image--dark" src="assets/generated/maintainer-overview-dark.svg" alt="Forge Activity with a selected pull request, live workspace, and coding-agent session in dark mode">
  <figcaption>Activity keeps the selected pull request beside its live workspace and coding-agent session.</figcaption>
</figure>

## Start here

If you are new to Forge, start with the [Quick start](quickstart.md). It walks
through installation, provider setup, repository selection, the first sync, and
workspace creation.

If you already have Forge running, jump straight to a task:

- [Follow activity across repositories](workflows/activity.md)
- [Triage an issue](workflows/issue-triager.md)
- [Review a pull request](workflows/code-reviewer.md)
- [Browse repository source](workflows/repositories.md)
- [Work in local sessions](workflows/workspaces.md)
- [Work through a worklist](workflows/worklists.md)
- [Read and edit local Docs](workflows/docs.md)
- [Run daily maintainer workflows](workflows.md)
- [Connect Roborev, Kata, or Docs](integrations.md)
- [Change repositories, tokens, or modes](configuration.md)
- [Fix startup, authentication, or sync problems](troubleshooting.md)

## Main views

**Activity** gathers recent comments, reviews, commits, and state changes in
one queue. Switch between threaded and flat mode, filter the list, and inspect
item details without losing your place.

**PRs** and **Issues** put lists, details, discussion, provider actions, and
workspace creation in one view. Actions that a provider does not support remain
visible but unavailable.

**Workspaces** opens durable local shells and configured agents against
repository worktrees, and **Repos** lets you browse branches, tags, files,
previews, and file history. When you open the **Reviews** panel in a pull
request workspace, it connects to a running Roborev daemon. **Docs** reads,
edits, pulls, and publishes registered Markdown folders. Kata issues appear
where you link them to a pull request, provider issue, or workspace rather than
in a separate mode.

## Advanced use

- [Command reference](commands.md)
- [Historical activity archives](archive.md)
- [Federated fleets](federated-fleet.md)
- [GitHub CLI shim for agents](gh-shim.md)

Forge runs on your machine. Your provider, Kata daemons, and local files remain
the source of truth.
