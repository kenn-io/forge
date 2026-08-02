# Provider Readiness Onboarding Design

## Goal

Put a provider-aware readiness checkpoint before repository selection so an
empty Kenn Forge installation explains how its code forge connects, remains
usable without `gh`, and preserves a focused route to synced pull requests and
a workspace.

## Scope

Replace the GitHub-only first step of the focused onboarding flow. The progress
rail keeps five milestones, but the first label changes from `GitHub ready` to
`Code forge`. Repository selection, first sync, pull-request selection, and
workspace launch remain the later milestones.

This change does not add credential APIs, install CLIs from the browser, or
duplicate provider host and token forms. Repositories settings remains the
source of truth for provider configuration.

## Readiness step

The first screen is titled `Connect a code forge` and is shown whenever the
flow starts with no configured repositories, including when `gh` is already
authenticated. It presents the supported provider paths as a compact list
rather than a grid of repeated cards:

- GitHub reports the detected `gh` installation, authentication state, host,
  and user. An authenticated session offers `Continue with GitHub`; a missing
  or unauthenticated CLI explains the local command and offers `Check again`.
- GitLab reports detected `glab` readiness when available and sends repository
  configuration to Repositories settings.
- Forgejo and Gitea explain that their host and token are configured through
  Repositories settings and use the same settings handoff.

The existing Repositories settings page opens its repository panel by default.
The onboarding lifecycle stays `active` during this handoff. After the user
configures any repository and returns to an eligible provider route, onboarding
resumes at first sync using the configured provider and host identity.

GitHub repository discovery remains inline because the current
`/platform/user-repositories` onboarding endpoint implements authenticated
GitHub discovery. The readiness screen must not imply equivalent automatic
discovery for providers that use the existing import flow. The picker requests
the endpoint's bounded 1,000-repository maximum and keeps a Repositories
settings escape for accounts whose target repository is not discoverable.

## State and recovery

The readiness step has a separate in-memory confirmation from CLI readiness.
An authenticated `gh` session is ready but does not skip the page; choosing
`Continue with GitHub` advances to repository selection. Reloading before a
repository is saved may show readiness again. This avoids expanding the
persistent onboarding schema for a transient confirmation.

Unknown tooling, a failed tooling probe, a missing CLI, unauthenticated CLI,
and failed repository discovery all render recoverable states. None may throw
out of the component, dismiss onboarding, or record completion. `Check again`
may verify a newly authenticated GitHub session through repository discovery,
matching the existing behavior.

## Layout and copy

The page keeps the restrained setup shell and product typography. Provider
rows use icons, status text, and separators; semantic color supplements text
and never carries readiness alone. The status group and action group are
separate vertical blocks with at least `var(--space-5)` between them. This gap
must remain when no command block or inline error is rendered, fixing the
current fused card-and-buttons state.

At narrow widths, provider rows and their actions stack without horizontal
overflow. Focus moves to the next step heading after an explicit continue.
Every action remains a native button or link with visible focus treatment.

## Verification

- A focused component test proves authenticated GitHub still stops at the new
  readiness page until the user continues.
- Component tests prove missing `gh` remains stable, the settings handoff keeps
  onboarding active, and configured repositories resume at sync.
- A real-backend Playwright scenario opens Repositories settings from a
  non-GitHub provider path, configures a repository, returns to an eligible
  route, and proves first sync uses the complete provider and host identity.
- The existing real-backend onboarding test continues to prove repository
  persistence, interrupted resume, sync, pull loading, and workspace creation.
- Isolated Playwright captures document the missing-tool readiness screen and
  the authenticated GitHub repository-selection screen without live user data.
- Svelte autofix, frontend checks, the full Vitest suite, the affected
  Playwright flow, and the production build run after the final edit.
