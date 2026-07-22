# Integration Test Selection

## Problem

The integration lane currently runs `go test -tags integration ./...`. Go build
tags add the integration-tagged test files to each package, but they do not
exclude ordinary test files. As a result, CI runs almost the full Go suite a
second time before executing the tests that require real Git repositories.

## Decision

Integration-tagged top-level tests use the `TestIntegration...` naming prefix.
Both `make test-integration` and the matching CI step keep the `integration`
build tag and add `-run '^TestIntegration'`.

The build tag remains responsible for compiling integration-only test files and
helpers. The name filter is responsible for selecting only their top-level
tests. The ordinary Go test lane remains unchanged and continues to exclude the
tagged files.

## Scope

Rename the existing top-level tests in the five files guarded by
`//go:build integration`. Do not change their assertions, fixtures, subtest
names, or production code. Update both local and CI integration commands so
they remain equivalent.

## Verification

- Before the change, demonstrate that a tagged package lists both ordinary and
  integration tests.
- After the change, list or run tests with `-tags integration -run
  '^TestIntegration'` and confirm that only integration-prefixed tests are
  selected.
- Run `make test-integration` and the ordinary affected package tests to confirm
  the two lanes remain distinct and passing.
