# GitHub Asynchronous Merge Design

**Date:** 2026-08-03
**Status:** Approved for implementation

## Goal

Merge GitHub pull requests through GitHub's supported asynchronous merge API so
GitHub-native stacked pull requests can be merged from Kenn Forge without
weakening the existing reviewed-head guarantee.

## Background

Kenn Forge currently sends GitHub merges through
`PUT /repos/{owner}/{repo}/pulls/{pull_number}/merge`. GitHub rejects native
stack members at that endpoint and requires
`PUT /repos/{owner}/{repo}/pulls/{pull_number}/merge-async`, introduced in API
version `2026-03-10`.

The asynchronous endpoint supports both stacked and unstacked pull requests.
For a stack member, GitHub atomically merges every unmerged pull request from
the bottom of the stack through the requested member. This remains subject to
Kenn Forge's existing mid-stack merge policy.

GitHub also offers a server-side cascading **Rebase stack** action on its
website. That action is not exposed through the documented token-authenticated
REST or GraphQL APIs. GitHub's website uses private, browser-session-bound
`page_data/stack_rebase` endpoints. Kenn Forge will not depend on those private
endpoints or introduce browser-cookie authentication into the daemon.

## Provider Behavior

All GitHub merges will use the asynchronous endpoint, regardless of whether
Kenn Forge currently recognizes the pull request as a native stack member.
Using one path avoids a stale stack-membership check and matches GitHub's
documented support for ordinary pull requests at the same endpoint.

The request will:

- send `X-GitHub-Api-Version: 2026-03-10` for this operation;
- pass the selected `merge`, `squash`, or `rebase` merge method;
- pass the current commit title and message;
- pass the reviewed head as `sha` so GitHub cancels the operation if the head
  moves before execution; and
- request `direct_merge` to preserve Kenn Forge's existing immediate-merge
  semantics rather than silently enqueueing a merge queue operation.

The provider will keep the existing synchronous Kenn Forge merge contract by
polling `GET /repos/{owner}/{repo}/pulls/{pull_number}/merge-async/{uuid}` after
a `202 pending` response. Polling will honor request cancellation, use a bounded
backoff, and stop at GitHub's terminal `merged`, `enqueued`, or `failed` state.

A terminal `merged` result returns the merge commit SHA and message through the
existing `platform.MergeResult`. A `failed` result becomes a provider error
whose detail preserves GitHub's message. An unexpected `enqueued` result is not
reported as merged because Kenn Forge requested a direct merge and does not yet
model merge-queue completion.

GitHub returns `409` when another asynchronous merge request already exists for
the pull request. Kenn Forge will surface that as a conflict rather than adopt
the UUID: the existing request may have a different merge method, commit
message, or reviewed head, and the response does not contain enough information
to prove that its options match the user's request.

## Stack Rebase Boundary

Kenn Forge will not automatically rebase a non-linear native stack in this
change. When GitHub rejects an asynchronous merge because the stack must be
rebased, Kenn Forge will preserve GitHub's exact failure message so the user can
run GitHub's **Rebase stack** action or rebase and push with `gh stack`.

Kenn Forge will not:

- call GitHub's undocumented website endpoints;
- acquire or persist GitHub browser cookies or CSRF state;
- run a local cascading rebase and force-push as a substitute for the native
  server action; or
- fall back to the synchronous merge endpoint after an asynchronous failure.

If GitHub publishes a token-authenticated server-side stack-rebase endpoint, it
can be designed as a separate provider capability with its own reviewed-head,
conflict, polling, and UI contracts.

## Deferred Merge Integration

The existing deferred **merge after CI** worker already completes through the
same provider merge method as immediate merges. It will therefore use the
asynchronous GitHub path automatically once pending checks pass. Its queueing,
cancellation, supersession, and event-ordering contracts remain unchanged.

## Error Handling

HTTP failures and asynchronous terminal failures must continue through Kenn
Forge's provider error classification. In particular:

- a moved reviewed head remains `stale_state`;
- an already-running asynchronous request is a conflict;
- GitHub validation, permission, branch-protection, and required-rebase
  messages remain visible to the user;
- transport failures remain provider-call failures; and
- cancellation or polling exhaustion must never be recorded locally as a
  successful merge.

The server must update the local pull request to merged only after the provider
returns a terminal `merged` result.

## Testing

Focused GitHub client tests will cover:

- the `merge-async` route, API-version header, request body, merge action, and
  reviewed-head binding;
- an immediate `merged` response;
- `pending` followed by `merged` polling;
- a terminal failure, including a stack-rebase-required message;
- stale-head classification;
- a `409` existing asynchronous operation;
- cancellation while polling; and
- an unexpected merge-queue result that must not be recorded as merged.

Existing provider and pull API tests remain the regression suite for server
error mapping, local state updates, mid-stack safeguards, and deferred merges.
No public Kenn Forge route or frontend response shape changes are required.
