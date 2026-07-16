# Single-Migration PR Enforcement Design

## Goal

Collapse the archive schema into its one PR-local migration and prevent future pull requests from introducing more than one migration identity.

## Migration history

`000039_historical_activity_archive` will directly create the final archive schema. The PR-local fix-up migrations `000040` through `000043` will be removed. Because none of these migrations shipped, the consolidated migration will contain no compatibility rebuilds for their intermediate schemas.

The down migration will reverse the final `000039` schema: drop archive tables before their parents and remove the snapshot-revision columns added to the existing issue and merge-request tables.

## Enforcement boundary

An `.up.sql` and `.down.sql` pair with the same numbered basename is one migration identity. A pull request fails when its delta from its immediate base contains more than one new migration identity.

Local pre-commit checks continue to inspect staged migration changes. CI uses `github.event.pull_request.base.sha` as the immediate base, computes PR-owned changes from its merge base with `HEAD`, and separately uses the current base tree for immutability and duplicate-number checks. This excludes lower-branch changes added after the child diverged while keeping migrations owned by the lower PR immutable to the child.

## CI integration

The lint checkout will fetch enough history for the PR base commit. `make guardrail-check` will run the migration-history checker with the immediate PR base SHA. On non-PR events the environment value is empty, so a clean checkout performs the existing staged-only check.

## Migration guide

`context/db-migrations.md` will retain short summaries of shipped-migration immutability and one migration per PR, then name the checker as the enforcement for immediate-base edits, duplicate numbers, and PR migration count. Procedural bullets duplicated by the checker will be removed; guidance requiring human judgment or schema knowledge will remain.

## Tests

Checker tests will cover multiple migrations staged together, multiple migrations spread across commits, one migration added by a child on top of a parent migration, and a parent-only migration added after the child diverged. Existing tests continue to cover edits to base migrations, duplicate numbers, and alternate hook indexes. Database tests validate the consolidated `000038 → 000039` migration through the existing `db.Open()` path.

## Non-goals

This change does not introduce compatibility handling for the discarded intermediate archive schemas, add a migration-file pairing rule, or make local hooks discover GitHub stack metadata.
