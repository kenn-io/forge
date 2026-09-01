---
title: Overview
---

# Kenn Forge

Forge gives you one local console for repository activity, pull requests,
issues, reviews, and agent working sessions. It syncs provider data into
SQLite and serves the UI from a single binary.

For a short visual introduction, start with the [Guide to Forge](/guide/).

<div class="docs-actions">
  <a class="md-button md-button--primary" data-download-current href="https://github.com/kenn-io/forge/releases">Download latest release</a>
  <a class="md-button" href="quickstart/">Quick start</a>
</div>

<p class="docs-download-meta"><span data-download-version></span>Linux, macOS, and Windows</p>

Use Forge to answer four daily questions:

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

New users should follow the [Quick start](quickstart.md). It covers
installation, provider setup, repository selection, the first sync, and
workspace creation.

Returning users can jump to a task:

- [Follow activity across repositories](workflows/activity.md)
- [Triage an issue](workflows/issue-triager.md)
- [Review a pull request](workflows/code-reviewer.md)
- [Browse repository source](workflows/repositories.md)
- [Work in local sessions](workflows/workspaces.md)
- [Read and edit local Docs](workflows/docs.md)
- [Run daily maintainer workflows](workflows.md)
- [Connect Roborev, Kata, or Docs](integrations.md)
- [Change repositories, tokens, or modes](configuration.md)
- [Fix startup, authentication, or sync problems](troubleshooting.md)

## Main views

**Activity** collects recent comments, reviews, commits, and state changes.
Use threaded or flat mode, filter the queue, and open item details without
losing your place.

**Pulls** and **Issues** combine lists, details, discussion, provider actions,
and workspace creation. Unsupported provider actions remain visible but
unavailable.

**Workspaces** opens durable local shells and configured agents against
repository worktrees. **Repos** browses branches, tags, files, previews, and
file history. **Reviews** connects to a running Roborev daemon. **Docs** reads,
edits, pulls, and publishes registered Markdown folders. Kata issues appear
where they are linked to a pull request, provider issue, or workspace rather
than in a separate mode.

## Advanced use

- [Command reference](commands.md)
- [Historical activity archives](archive.md)
- [Federated fleets](federated-fleet.md)

Forge runs on your machine. Your provider, Kata daemons, and local files
remain the source of truth.
