# Gitea review synchronization follow-ups

## Goal

Correct Gitea inline-review context-line normalization and ensure the Gitea
1.24.6 container test exercises the same server-version discovery path used in
production.

## Scope

This follow-up changes only the Gitea review-thread normalizer, Gitea test
client options, their focused package tests, and the existing Gitea container
test setup. It does not change the validated 1.24.6 capability floor, Forgejo
behavior, archive orchestration, or persisted review-thread schema.

## Context-line normalization

Gitea review comments use `position` for the new-side coordinate and
`original_position` for the old-side coordinate. A comment with both values is
a context-line comment. The normalizer currently checks the old coordinate
first, so it discards the new coordinate and reports the comment as a
left-side deletion.

The normalizer will evaluate coordinate shapes in this order:

1. When both coordinates are positive, preserve both pointers, keep the
   canonical line on the right/new coordinate, and set `LineType` to
   `"context"`.
2. When only the old coordinate is positive, report a left-side deletion.
3. When the new coordinate is positive, report a right-side addition.

A focused normalization regression test will construct a Gitea SDK review
comment with both coordinates and assert the complete platform range. The test
must fail against the current old-first implementation before the production
change is made.

## Version discovery test seam

`WithBaseURLForTesting` currently changes two independent inputs: the server
URL and an assumed Gitea version of 1.26.0. This makes every test client skip
the SDK's `/api/v1/version` request, including the real Gitea 1.24.6 container
test that is intended to validate the advertised minimum version.

`WithBaseURLForTesting` will configure only the normalized base URL. A new
`WithServerVersionForTesting` option will explicitly set the SDK version for
unit tests whose HTTP fixtures intentionally do not implement version
discovery. Existing package tests will opt into the assumed version where
needed. The existing version-floor test will use the explicit option instead
of mutating private client options inline.

The Gitea container test will pass the base URL without a forced version. The
SDK will therefore discover `1.24.6` from `/api/v1/version` during client
construction, and the resulting capabilities will gate review-thread sync and
archive inline-comment coverage from the production-discovered value.

## Verification

Focused unit coverage will prove that:

- both-positive coordinates normalize as a context line with both pointers;
- URL-only client construction requests the version endpoint and enables the
  review-thread capability at the discovered 1.24.6 floor;
- explicit forced versions still cover below-floor, at-floor, and newer
  capability behavior without requiring unrelated HTTP fixtures to implement
  version discovery.

After the focused red-green cycles, run the Gitea provider package tests and
the real `gitea/gitea:1.24.6` container test. The container result is the
cross-layer proof that production version discovery, regular review-thread
sync, archive hydration, persisted metadata, coverage reporting, and inline
activity reporting continue to work together at the minimum supported release.

## Alternatives considered

Updating every Gitea `httptest` handler to serve `/api/v1/version` would make
all unit tests probe, but it would add an unrelated request to many narrowly
scoped fixtures and disturb request-count and budget assertions. Keeping the
implicit forced version and adding an option that re-enables probing would
reduce call-site edits, but it would retain the coupling that caused the
container coverage gap. Separating the two inputs is the smallest explicit
interface and keeps each test responsible for the behavior it intends to
exercise.
