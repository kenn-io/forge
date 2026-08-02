# ADR 0003: Provider-ready first-run activation

Date: 2026-08-02

## Status

Accepted

## Context

The focused onboarding path originally treated an authenticated GitHub CLI as
the only successful entry into repository selection. The missing-`gh` state
was recoverable, but its GitHub-only framing made the supported provider model
look narrower than the product and sent other provider users out of the flow.
The recovery card also visually merged with its action row when no command
block was present.

## Decision

Start first-run onboarding with an explicit, provider-aware code-forge
readiness step before repository selection. Present GitHub, GitLab, Forgejo,
and Gitea as supported paths. Keep credentials, provider hosts, and non-GitHub
repository import in the existing Repositories settings surface instead of
duplicating configuration forms inside onboarding.

An authenticated `gh` session may continue to inline GitHub repository
discovery. Other provider paths preserve the active onboarding lifecycle while
the user visits Repositories settings, then resume at first sync after a
repository is configured. Missing tools, unauthenticated CLIs, and tooling
probe failures remain stable recovery states and never count as completion.
The readiness status and its action row use separate vertical groups so the
next action is visually distinct.

The route trigger, provider/host identity, dismissal, and workspace completion
rules from ADR 0002 remain unchanged.

## Consequences

Every new user sees the provider boundary before choosing repositories, even
when GitHub is already ready. GitHub remains the shortest path because the
existing user-repository endpoint supports authenticated `gh` discovery.
Provider credential policy stays centralized in Repositories settings, and
onboarding does not become a second settings implementation.
