# Detached HEAD Divergence Design

## Problem

kenn-forge intentionally permits a detached `HEAD` as the last-resort state
for a managed worktree. Git reports `fatal: HEAD does not point to a branch`
when the divergence and unpushed-commit probes ask that worktree for its
upstream. kenn-forge currently treats that expected result as an unexpected
probe failure, producing recurring fleet warnings and workspace debug errors.

## Design

Extend the existing no-upstream error classification in
`internal/workspace/divergence.go` to recognize Git's exact detached-`HEAD`
message. Both `WorktreeDivergence` and `WorktreeUnpushedSHAs` already share this
classification and will return their documented unavailable result: zero data,
`ok=false`, and no error.

This keeps detached worktrees eligible for diff sampling, adds no routine Git
subprocess, and leaves all other `rev-list` failures visible. It also avoids a
sampler-specific exception that would leave workspace enrichment noisy.

## Verification

Add real-Git regression coverage that detaches an existing test worktree and
asserts that both upstream probes report unavailable data without error. Run
the focused workspace package tests, followed by the repository's applicable
short verification before committing the implementation.
