# GitHub App Merge Settings Repair

## Problem

GitHub can omit `allow_squash_merge`, `allow_merge_commit`, and
`allow_rebase_merge` from a repository response when an installation token has
Metadata access but not Administration access. Forge currently converts those
absent fields to `false` and persists an authoritative-looking all-disabled
snapshot. The pull request UI then labels the action as `Merge`, even when the
repository allows only squash merging.

## Design

Repository merge settings are permission-sensitive optional data. The GitHub
adapter must preserve the distinction between a returned `false` value and an
absent field.

In split-auth mode, Forge continues to read general repository metadata with
the GitHub App installation token. Its existing user-credential repository read
will overlay both viewer permissions and merge settings. Forge will persist
merge settings only when the source response contains all three merge-method
fields. A failed user-credential read, or a response that still omits any merge
field, leaves the last verified settings unchanged.

A successful user overlay is authoritative even when all three returned values
are `false`. Therefore the repair does not infer that an all-false database row
is corrupt. Instead, every normal repository refresh persists the explicit
user-authenticated values, which automatically repairs rows previously damaged
by omitted App fields.

The provider-neutral repository snapshot will continue to represent unknown
merge settings with a nil `MergeSettings` value. This keeps omission handling
at the GitHub boundary and lets the existing database observation writer retain
stored values for unknown snapshots.

## Failure Behavior

General metadata synchronization remains available if the user-credential
overlay fails. Forge clears viewer-specific permission information as it does
today, treats merge settings as unknown, logs the overlay failure, and does not
erase stored settings.

The UI must not claim that `Merge` is available when the API returns no enabled
method. The merge action will be unavailable for that defensive state. This is
not the repair mechanism; it prevents misleading behavior if an unknown or
invalid snapshot reaches the UI again.

## Verification

Regression coverage will prove that:

- an App response with omitted merge fields produces unknown merge settings;
- a successful user overlay supplies explicit merge settings;
- a failed or incomplete overlay preserves previously stored settings;
- a later explicit overlay repairs a previously stored all-false row; and
- the pull request UI does not label the all-false state as `Merge`.

Focused GitHub client/sync tests and the relevant pull-detail component test
will cover the provider-to-database and database-to-UI boundaries.
