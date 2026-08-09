# ADR 0004: Live Worktree Registration Authorizes Cleanup

## Status

Accepted

## Context

kenn-forge writes an ownership marker into linked-worktree metadata when it
creates a workspace. Workspaces created before that marker was introduced have
valid persisted records and live Git registrations but cannot be deleted or
retried because cleanup treats the missing marker as unproven ownership.

## Decision

An exact live linked-worktree registration in the repository resolved for the
persisted workspace is sufficient authority for cleanup. The ownership marker
is not required for workspace deletion or retry cleanup.

Cleanup will still preserve a checkout that belongs to another repository, a
standalone replacement clone, or a path no longer registered by the resolved
repository. The cleanup operation will recheck registration under the
repository lock before removing the worktree and managed branches.

Ownership markers remain part of worktree creation and rollback, where they
distinguish the generation kenn-forge just created from a replacement that may
have appeared during a failed setup.

## Consequences

- Pre-marker workspaces are deletable without migration or manual metadata
  repair.
- A manually created linked worktree at the exact persisted workspace path in
  the expected repository is treated as the workspace and can be deleted.
- Replacement repositories and ownership changes during cleanup remain
  protected by repository identity and the locked registration recheck.
