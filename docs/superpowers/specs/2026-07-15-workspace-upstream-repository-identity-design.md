# Workspace Upstream Repository Identity

## Purpose

Workspace branches must never acquire a push upstream from branch names or
commit equality alone. Forks preserve commit IDs, so an untrusted fork can have
the same head branch name and commit as a base-repository branch without being
authorized to push to that base branch.

This change also makes repository comparison preserve GitLab's complete nested
namespace so same-repository merge requests under paths such as
`group/subgroup/project` can be classified and healed correctly.

## Head Repository Identity

PR head identity has three states:

- **same repository**: a non-empty, parseable head clone URL resolves to the
  workspace's provider, normalized host, and complete base repository path;
- **fork**: that provider-aware identity differs from the workspace's base
  repository identity;
- **unknown**: the clone URL is empty or cannot be normalized.

Unknown is a conservative safety state for historical workspace rows,
temporarily incomplete provider payloads, or malformed clone metadata. Direct
PR workspace creation is expected to receive usable head-repository metadata,
but must remain safe if that expectation is violated. Kata-task and provider-
issue workspaces may legitimately remain unknown until refresh maps an
associated PR and supplies its head-repository evidence.

The existing nullable `MRHeadRepo` field will encode the distinction without a
schema migration:

- `nil` means confirmed same repository;
- a non-nil empty string means unknown;
- a non-nil URL means confirmed fork.

Code that presents fork information to users must require a non-empty value;
non-nil alone is only the setup-routing signal for a fork-safe checkout.

## Creation And Setup

Workspace creation classifies the current merge-request row and persists the
tri-state representation. Before setup or retry mutates Git state, the manager
reclassifies from the current merge-request row so older rows that collapsed
unknown into `nil` do not retain the unsafe assumption.

Confirmed same-repository heads may use `origin/<head>` and configure it as the
upstream. Confirmed forks and unknown heads use the provider's merge-request
head ref, create a branch without an `origin/<head>` upstream, and never reuse a
local-base checkout as proof of ownership.

If a fork or unknown head cannot be materialized from the provider's
merge-request ref, setup fails rather than falling back to a same-named base
branch. Matching SHAs may confirm checkout content but never authorize a push
target.

## Observer Healing

The pushed-head observer classifies repository identity from the current
merge-request row rather than trusting the workspace's creation-time pointer.
It configures a missing upstream only when all of these are true:

- the current head clone URL proves the head is in the base repository;
- the checked-out branch is the PR head branch or middleman's synthetic PR
  branch;
- `origin/<head>` exists.

Fork and unknown states remain untracked. A historical workspace, Kata-task
workspace, or provider-issue workspace may heal later when refresh maps its
associated PR and supplies explicit same-repository metadata.

## Clone URL Normalization

Repository identity normalization keeps the provider, normalized host, and the
complete repository path. It supports URL clone forms and SCP-style SSH forms
while trimming surrounding slashes and a final `.git` suffix.

Examples:

- provider `gitlab` plus `https://gitlab.com/group/subgroup/project.git`
  becomes `gitlab/gitlab.com/group/subgroup/project`;
- `git@gitlab.com:group/subgroup/project.git` becomes the same identity;
- existing two-segment GitHub and self-hosted identities remain unchanged.

Local filesystem paths and malformed clone strings remain unparseable and
therefore classify as unknown.

## Tests

Regression coverage will prove:

- missing head-repository metadata cannot configure an upstream even when a
  same-named `origin` branch and the workspace have the same SHA;
- confirmed same-repository workspaces continue to configure the correct
  upstream;
- confirmed forks remain fork-safe;
- nested-group GitLab merge requests classify as same repository during
  creation and can be healed by the observer;
- Kata-task and provider-issue workspaces remain untracked until refreshed
  associated-PR metadata proves a same-repository head;
- malformed or absent clone metadata remains untracked.

Workspace-package tests cover repository classification, Git branch behavior,
observer healing, and refresh-mapped issue/Kata workspaces. A server e2e test
uses the generated HTTP client, real SQLite, and real temporary Git repositories
to retry a legacy unknown workspace and verify its branch remains untracked.
No provider container is required because the defect is deterministic local
identity and Git configuration logic rather than upstream API-shape drift.

## Scope

This change does not add a migration, compatibility alias, alternate push path,
or provider-specific fallback. It changes only workspace head-repository
classification, clone URL normalization, upstream authorization, and focused
regression coverage.
