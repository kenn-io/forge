# Single-Migration PR Enforcement Design

## Goal

Collapse the archive schema into its one PR-local migration and prevent future pull requests from introducing more than one migration pair.

## Migration history

`000039_historical_activity_archive` will directly create the final archive schema. The PR-local fix-up migrations `000040` through `000043` will be removed. Because none of these migrations shipped, the consolidated migration will contain no compatibility rebuilds for their intermediate schemas.

The down migration will reverse the final `000039` schema: drop archive tables before their parents and remove the snapshot-revision columns added to the existing issue and merge-request tables.

## Enforcement boundary

An `.up.sql` and `.down.sql` pair with the same numbered basename is one migration identity. A pull request fails when its delta from its immediate base contains more than one new migration identity.

Local pre-commit checks continue to inspect staged migration changes. CI checks the complete pull-request delta using `github.event.pull_request.base.sha`; this avoids counting migrations inherited from lower branches in a stacked PR. A migration already present in that immediate base belongs to the lower PR and remains immutable to the current PR.

## CI integration

The lint checkout will fetch enough history for the PR base commit. `make guardrail-check` will run the migration-history checker with the immediate PR base SHA. Pushes to `main` and manual workflow runs will use `origin/main` and produce an empty migration delta when no new migration is being proposed.

## Migration guide

`context/db-migrations.md` will retain short summaries of shipped-migration immutability and one migration per PR, then name the checker as the enforcement for immediate-base edits, duplicate numbers, and PR migration count. Procedural bullets duplicated by the checker will be removed; guidance requiring human judgment or schema knowledge will remain.

## Tests

Checker tests will cover multiple migrations staged together and multiple migrations spread across commits when a PR base is supplied. Existing tests continue to cover edits to base migrations, duplicate numbers, and alternate hook indexes. Database tests will validate the consolidated migration through the existing `db.Open()` paths.

## Non-goals

This change does not introduce compatibility handling for the discarded intermediate archive schemas, add a migration-file pairing rule, or make local hooks discover GitHub stack metadata.
