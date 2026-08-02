# ADR 0002: Focused GitHub first-run activation

Date: 2026-08-02

## Status

Accepted

## Context

An empty Kenn Forge installation previously exposed disconnected empty states.
The onboarding prototype comparison identified a focused linear path as the
clearest way to reach useful maintainer work with an already-installed `gh`.
Kata, Docs, repositories, settings, reviews, and existing workspace routes are
independent product surfaces and must remain directly reachable.

## Decision

Use the focused five-step GitHub quickstart on provider Activity, pull-request,
issue, and focus entry routes after settings load with no configured
repositories. An active interrupted flow may resume on those routes after its
repositories have been saved. Never replace non-provider, configuration,
review, or workspace routes with onboarding.

Repository discovery targets the exact authenticated GitHub host and carries
the full provider/host identity into configuration. Users can dismiss setup for
the current browser session or open regular settings for another provider.
Only a successful workspace create/open handoff records activation complete;
opening a PR view or reaching a genuine zero-PR result is a dismissal, not
completion. A completed marker is ignored when the server again reports zero
repositories.

## Consequences

The first-run path remains deliberately GitHub-specific while the regular
configuration UI remains the provider-neutral escape path. Interrupted setup
can re-enter from server-backed repository state without blocking unrelated
application modes. Sync and pull-loading failures remain recovery states rather
than being presented as successful empty results.
