# Disabled Repository Feature Sync Cooldown

## Problem

Middleman models issue and merge-request support as provider-wide capabilities. A
repository can disable one of those features even when its provider supports it.
Today, a definitive provider response such as GitHub's `410 Issues are disabled`
is treated as a generic partial sync failure.

That generic classification creates two kinds of waste:

- closure detection continues fetching other stored items after the first
  definitive disabled-feature response;
- `failedRepos` invalidates the affected list ETag and forces the same scope to
  retry on the next normal sync cycle.

The result is a burst of doomed item requests followed by eager retries that
consume API budget without improving local data.

## Goals

- Represent a disabled repository feature as a stable, typed platform error.
- Stop work for the affected repository scope after the first definitive
  disabled-feature response.
- Defer background probes for that repository scope for 24 hours.
- Continue syncing unaffected scopes and repositories at their normal cadence.
- Let an explicit user-triggered sync bypass the disabled-feature cooldown.
- Preserve provider and host identity in classification and scheduling state.

## Non-goals

- Persist disabled-feature cooldowns across process restarts.
- Infer disabled features from ambiguous permission or not-found responses.
- Change transient failure retries, provider rate-limit gates, archive cadence,
  or the normal repository sync interval.
- Add compatibility aliases for the new error category.

## Error Model

Add `repository_feature_disabled` to `platform.PlatformErrorCode`. A typed
`platform.Error` in this category carries:

- `Provider` and `PlatformHost`;
- the affected capability, either `issues` or `merge_requests`;
- the original provider error in `Err` for logs and diagnostics.

This category is distinct from `unsupported_capability`. The latter means a
provider implementation cannot perform an operation. The new category means
the provider supports the operation, but the target repository has disabled
that feature.

Provider code classifies only definitive signals. GitHub's issue endpoint `410`
response with the disabled-issues condition is one such signal. Other adapters
may map their provider-specific equivalent when the endpoint and response make
the condition unambiguous. A generic `403`, `404`, authentication failure,
transport failure, or unexpected response must retain its existing category.

## Sync Scheduling

The syncer keeps an in-memory next-probe map keyed by the full repository scope:

```text
(provider, platform_host, owner, repo, issues|merge_requests)
```

The key uses the same provider metadata and repository normalization rules as
the rest of the sync engine. A disabled issue scope must not suppress merge
requests, and state for one provider host must not affect another host with the
same owner and repository names.

When a background index sync reaches a cooled-down scope, it skips that scope
without reporting a repository failure. Once 24 hours have elapsed, the next
background cycle probes it normally. A clean scope sync clears any prior
cooldown. Process restart clears the map and permits one fresh probe.

User-triggered repository or global syncs bypass the cooldown. If the feature
has been re-enabled, the successful sync clears the stale cooldown. If it is
still disabled, the typed result refreshes the 24-hour deadline.

## Failure Flow

Issue and merge-request index paths inspect scope errors before converting them
to `PartialSyncError`:

1. A definitive disabled-feature error stops the remaining work for that scope.
2. The syncer records the scope's next eligible probe time.
3. The scope is not added to `failedRepos`, so no ETag invalidation or eager
   recovery retry occurs.
4. Other scopes continue in the same repository sync.
5. The repository sync completes without a generic failure caused solely by the
   disabled scope.

All other failures keep their current behavior. Per-item transient failures
still set the appropriate `failedRepos` bit, and hard repository failures still
surface through sync health.

The first disabled-feature response must terminate loops over stored closures or
open items. This prevents a repository with many locally open issues from
issuing one doomed request per item before the cooldown is established.

## Logging and Status

The first classification logs one structured informational event containing the
repository identity, scope, and next probe time. Cooldown skips may log at debug
level. The raw provider error remains available as the wrapped cause, but the
expected disabled condition is not emitted repeatedly as an error.

No API or frontend contract changes are required. Existing sync results should
not present an expected disabled scope as a partial repository failure.

## Testing

Focused tests will verify:

- the platform error constructor and `errors.Is` behavior for
  `repository_feature_disabled`;
- definitive provider responses are classified while ambiguous permission,
  not-found, and transient responses are not;
- item processing stops after the first disabled-feature error;
- only the affected scope receives a 24-hour cooldown;
- another repository, provider, or host with the same names remains eligible;
- a background sync skips a cooled-down scope without marking `failedRepos`;
- an explicit sync bypasses the cooldown;
- a successful probe clears the cooldown;
- ordinary partial failures retain ETag invalidation and next-cycle retry
  behavior.

Verification will run the focused platform/provider tests and the affected
`internal/github` sync tests with `-shuffle=on`.
