# GitHub Credential-Aware Rate Accounting Design

## Goal

Stop treating middleman's local host-wide sync ceiling as GitHub quota, and
schedule GitHub work from the actual REST and GraphQL pools of the credential
that will perform each request. Keep a high local ceiling only as an emergency
guard against runaway background traffic.

## Problem

GitHub reads can use an account-scoped App installation token while writes,
notifications, and repos owned by accounts without an App installation use the
user credential. GitHub assigns independent quotas to each credential and to
the REST and GraphQL resources.

Middleman currently collapses those independent pools into one host-keyed
`SyncBudget`. The periodic `/rate_limit` request is also ownerless. Owner-scoped
App token resolution therefore skips the App candidate and falls through to the
user credential, after which the user quota snapshot overwrites the tracker used
for App-backed reads. The status surface can consequently alternate between App
and user values while the local ceiling stops background work even when GitHub
has substantial capacity.

## Decisions

- GitHub's provider-reported quota is authoritative for scheduling.
- Quota identity is `(provider, host, credential identity, API resource)`.
- GitHub credential identities are non-secret stable labels:
  `github_app:<installation_id>` for App reads and `user` for the PAT/CLI
  credential. Raw tokens must never become keys, logs, API fields, or database
  values.
- REST and GraphQL remain separate resources.
- Normal response headers continuously update the exact credential/resource
  pool without extra provider requests.
- `/rate_limit` reconciliation runs at most once per credential every three
  minutes. One REST response supplies both REST and GraphQL quota facts; no
  separate GraphQL `rateLimit` query is needed.
- `sync_budget_per_hour` remains a process-local emergency ceiling and is set
  to `50000` in the maintainer's active config. It is not presented as GitHub
  remaining quota.
- The existing 200-request provider reserve remains the scheduling floor for
  each actual provider pool.
- No database migration or compatibility translation layer is introduced.

## Architecture

### Credential resolution

Token resolution returns the bearer token together with a safe credential
identity. `AuthTransport` preserves that identity in the authorized request
context passed to its base transport. The identity follows the candidate that
actually supplied the token, so a PAT fallback cannot be mislabeled as an App
request.

Mutation-marked and notification requests resolve to `user`. A repo-scoped read
for an owner covered by an installed App resolves to that App installation.
Other owners resolve to `user`.

### Provider quota registry

A GitHub quota registry owns in-memory provider state keyed by credential
identity and API resource. Each entry contains the provider limit, remaining
count, reset time, freshness, and request observations needed by scheduling and
status reporting.

REST and GraphQL transports parse GitHub's response headers after each wire
attempt and update only the resolved credential's matching resource. Internal
401 invalidation retries remain separate wire attempts and can update the
identity selected for their individual attempt.

The registry is process-local. After restart, entries begin unknown and become
known from ordinary response headers or the next bounded reconciliation. Brief
unknown state is acceptable and does not justify a schema migration.

### Snapshot reconciliation

The syncer groups configured GitHub owners by resolved credential identity. For
each App identity it calls `/rate_limit` with a representative matching owner
in context. It calls the same endpoint once with explicit user-auth context for
the user credential. Each credential is refreshed no more often than once per
three-minute interval.

A successful response updates both REST and GraphQL entries for only that
credential. A failed snapshot retains the last response-derived state and
reports the failure without erasing or replacing another credential's quota.

### Scheduling

Before provider work, the scheduler resolves the credential identity for the
repository owner and checks the provider resources that operation can consume.
GitHub archive hydration can touch both REST and GraphQL, so it requires safe
capacity in both known pools. Foreground work remains higher priority and keeps
the existing provider error handling.

Unknown or stale quota does not block explicit foreground work. Archive work
waits until ordinary response headers or reconciliation establish safe surplus.
When a pool reaches the 200-request reserve, work using that pool pauses until
its reset; unrelated credential pools remain eligible.

### Local emergency ceiling

`SyncBudget` remains independent from provider quota. Its transport reserves a
unit atomically before a counted wire attempt. A `304 Not Modified` response
refunds the reservation because it does not spend primary provider quota.
Archive reservations update total and archive spend atomically. Remaining is
therefore never negative.

Local exhaustion returns and displays a local-ceiling reason. Provider reserve
exhaustion continues to use provider rate-limit terminology and reset timing.

### Status API and UI

The rate-limit response exposes each provider credential pool separately under
its host, with a safe credential label and independent REST/GraphQL values. The
local emergency ceiling is a separate object with limit, spent, and remaining.
The status UI renders provider quota and local ceiling as distinct concepts and
never labels the local counter as GitHub remaining quota.

Generated OpenAPI and client artifacts are regenerated after the contract
change. This is an internal/thin-client API; callers migrate directly to the new
shape without a legacy response adapter.

## Failure Handling

- A credential-resolution failure fails that request using the existing typed
  authentication path; it does not silently relabel the request.
- A reconciliation failure leaves prior quota state intact and retries only
  after the normal bounded cadence.
- A provider pool at or below reserve pauses only work mapped to that pool.
- An unknown provider pool holds archive work but does not disable explicit
  foreground actions.
- A local ceiling exhaustion is reported separately from upstream rate limiting.
- Provider 429/reset-aware errors remain authoritative even if cached quota was
  briefly stale.

## Testing

Tests cover middleman-owned boundaries rather than GitHub or HTTP library
behavior:

- An ownerless snapshot regression fixture gives the App and user credentials
  different quotas and proves App reconciliation selects the App identity.
- Two owners sharing `github.com` prove App and user response headers cannot
  overwrite each other.
- REST and GraphQL responses for one credential update independent entries.
- Snapshot reconciliation runs at most once per credential in three minutes.
- Notification traffic updates the user pool and never the App pool.
- Concurrent local spend cannot make remaining negative, and a 304 refunds its
  reservation.
- Wire-level `/rate-limits` coverage proves provider pools and the local ceiling
  are separate response fields.
- Frontend status tests prove the two concepts remain visibly distinct.

Verification includes the focused token-auth, GitHub sync, server API, generated
client, and frontend status suites. Full affected frontend tests run after the
final frontend edit, as required before any push.

## Out of Scope

- Changing GitHub's 200-request provider reserve.
- Persisting credential-aware quota snapshots across restart.
- Adding compatibility aliases for the old rate-limit response.
- Changing mutation attribution away from the user credential.
- Generalizing credential-aware quota accounting to providers that do not yet
  expose multiple independent credentials on one host.
