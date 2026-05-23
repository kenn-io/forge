# Default-Branch Activity Design

## Summary

Show commits and detectable force-pushes on each repository's configured
default branch in the Activity view. These rows are visible by default because
they surface work that otherwise has no pull request or issue thread. Users can
hide them from the Activity filter menu.

The feed remains database-backed. Sync extracts default-branch activity around
the existing bare-clone fetch: it reads the previously observed branch tip,
fetches, resolves the new tip, persists recent rows in SQLite, and `ListActivity`
reads them through the same SQL-union path as pull requests, issues, comments,
reviews, and pull-request commits.

## Goals

- Surface direct pushes, squash/rebase landings, and merge commits on the
  default branch.
- Keep Activity endpoint latency predictable and avoid shelling out to Git on
  navigation, polling, or filter changes.
- Preserve one SQL-backed activity cursor across PR, issue, event, commit, and
  force-push rows.
- Use provider-neutral clone state and persisted repository identity rather
  than provider-specific commit APIs.
- Keep the implementation flat. Do not add a tiny subpackage only for this
  feature; prefer focused files and methods in existing packages unless a real
  ownership boundary emerges.

## Non-Goals

- Do not resolve git authors or committers to provider users in v1.
- Do not store provider commit URLs. Derive links at API serialization time
  from provider metadata and the commit SHA.
- Do not list every commit brought in through a merged side branch. The
  default-branch feed follows the first-parent chain.
- Do not synthesize force-push events when the upstream default branch changes.

## Data Model

Add a default-branch commit table:

```text
middleman_branch_commits
- id
- repo_id
- branch_name
- commit_sha
- author_name
- author_email
- authored_at
- committer_name
- committer_email
- committed_at
- subject
- created_at
- updated_at
```

Use `(repo_id, commit_sha)` as the unique key. The API continues to expose full
repository identity as `(platform, platform_host, owner, name)` through joins to
`middleman_repos`.

Store both authored and committed timestamps. `committed_at` is the Activity
sort key because it represents when the commit landed in the rewritten history;
`authored_at` can drift and would make rebased commits appear stale.

Add a branch-tip table:

```text
middleman_branch_tips
- repo_id
- branch_name
- tip_sha
- observed_at
- created_at
- updated_at
```

Use `(repo_id, branch_name)` as the unique key. This table tracks the last tip
seen for force-push detection and should not be folded into repository overview
state.

Add a default-branch force-push table:

```text
middleman_branch_force_pushes
- id
- repo_id
- branch_name
- before_sha
- after_sha
- detected_at
- created_at
```

Use a dedupe key such as `(repo_id, branch_name, before_sha, after_sha)` so a
retry after a partial sync does not duplicate the same detected rewrite.

Retain only rows needed by the maximum Activity time range, currently 90 days.
Prune older branch commits and force-push rows during sync so the tables do not
grow without bound. Historical commit rows from an old default branch may remain
until retention removes them.

## Sync Flow

Run branch-activity tracking around the existing clone fetch for a repo. The
clone manager remains the source of Git data.

For each repo:

1. Read the persisted default branch from `middleman_repos`.
2. Read the previous tip for `(repo_id, default_branch)` from
   `middleman_branch_tips`.
3. Run the existing clone fetch.
4. Resolve `refs/remotes/origin/<default_branch>` in the clone.
5. If there was a previous tip, run
   `git merge-base --is-ancestor <previous_tip> <current_tip>`.
6. If the previous tip exists and is not an ancestor of the current tip, insert
   a `default_branch_force_push` row with `before_sha`, `after_sha`, branch
   name, and detection time.
7. Walk first-parent commits on `origin/<default_branch>`:
   - incremental sync: `<previous_tip>..<current_tip>`
   - first sync or missing previous tip: commits since the 90-day retention
     boundary
8. Upsert commit rows with raw git author/committer metadata and timestamps.
9. Update `middleman_branch_tips` to the current tip.
10. Prune branch activity outside retention.

This ordering is important. Updating the tip before comparing ancestry loses
the force-push signal.

If the upstream default branch changes, do not compare the previous branch's
tip to the new branch's tip and do not create a force-push row. Start tracking
the new branch from its current tip. Previously persisted activity remains
queryable until retention removes it.

## Activity API

Extend `ListActivity` with two repo-level activity types:

- `default_branch_commit`
- `default_branch_force_push`

These rows have repository identity, branch name, SHA metadata, author text, and
timestamps, but no PR or issue number. The response model should make repo-level
activity explicit rather than stuffing branch commits into PR/issue fields.

Search should match commit subject, author name/email, committer name/email,
branch name, and SHA prefixes. Repo filters and time-window filters apply
through the same SQL query as other activity.

Commit links are derived at API serialization time. Use provider metadata and
the SHA to build the best known web URL for the provider/host; if no reliable
template exists, omit the link rather than persisting one.

## Filtering

Default-branch activity is shown by default.

The Activity filter menu gets a visibility option labeled `Hide default-branch
activity`. When enabled, it hides both `default_branch_commit` and
`default_branch_force_push` rows. Resetting hidden filters shows them again.

Existing item filters keep their semantics:

- `All` includes PRs, issues, and repo-level branch activity.
- `PRs` includes PR-level items/events only.
- `Issues` includes issue-level items/events only.

## Flat View

Flat view shows each default-branch commit and force-push as its own row.
Default-branch commit rows should display:

- repository
- branch
- short SHA
- subject
- raw author name
- committed time

Force-push rows should display:

- repository
- branch
- short before and after SHAs
- detected time

## Threaded View

Threaded view groups default-branch activity into one repo/branch thread, for
example `main updates on owner/repo`. The thread key includes provider, host,
repo, and branch name.

Commit runs inside that thread use the same rollup behavior as existing
pull-request commit runs. The helper must support repo-level branch commits
without requiring an item number.

Force-push rows appear inside the same repo/branch thread as distinct events and
are not folded into commit rollups.

## Provider Behavior

The feature is provider-neutral because it reads from the existing clone
manager. Provider APIs are not required for v1.

Provider-specific enrichments, such as force-push actor attribution, can be
added later behind capability checks if a provider exposes reliable data. v1
uses raw git metadata for commits and no actor for clone-detected force-pushes.

## Testing

Database tests:

- Mixed PR, issue, event, default-branch commit, and force-push activity sorts
  by the correct timestamp with stable cursors.
- Repo filters include branch activity only for matching provider-aware repo
  identity.
- Time-window filters use `committed_at` for commits and `detected_at` for
  force-pushes.
- Search matches commit subject, raw author/committer fields, branch name, and
  SHA prefixes.
- Retention pruning removes old branch commits and old force-push rows.

Git clone tests:

- First sync backfills first-parent commits within the 90-day retention window.
- Incremental sync walks `<previous_tip>..<current_tip>`.
- A first-parent merge commit appears as one default-branch row, while commits
  reachable only through the second parent do not appear as their own
  default-branch rows.
- A force-push fixture writes commit A, rewrites history so A is no longer
  reachable, and records one force-push row with the expected before/after SHAs.
- A default-branch rename from `master` to `main` starts tracking the new branch
  without recording a force-push event.

Server e2e tests:

- `/activity` returns default-branch commits by default using real SQLite.
- The hide filter excludes both default-branch commits and force-pushes.
- Response rows include provider-aware repo identity and do not fake PR/issue
  numbers for repo-level activity.

Frontend tests:

- The Activity filter menu exposes `Hide default-branch activity` and reset
  shows it again.
- Flat view renders branch commits and force-pushes as individual rows.
- Threaded view groups branch activity by repo/default branch and rolls up
  commit runs without requiring a PR or issue item number.
