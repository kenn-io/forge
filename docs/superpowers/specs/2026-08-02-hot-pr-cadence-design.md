# Hot PR Refresh Cadence Design

## Problem

The Activity fast-refresh loop runs every configured `active_pr_refresh_interval`,
but recently active PR selection also requires `detail_fetched_at` to be at least
that old. A foreground or startup fetch shortly before a loop tick can therefore
exclude a hot PR from that tick. The next opportunity is another full interval
later, turning a nominal two-minute policy into almost four minutes.

Notification activity cannot close this gap for self-authored changes because
GitHub does not necessarily create a notification for the author.

## Considered approaches

1. **Make the scheduler tick authoritative for hot PRs (selected).** Include every
   PR active within 30 minutes on each fast-refresh pass. Keep the existing
   `detail_fetched_at` eligibility check for warm PRs, which refresh every five
   minutes. This directly matches the configured policy and keeps the current
   scheduler, budgets, throttling, and conditional requests.
2. Poll eligibility more frequently than the configured interval. This avoids
   extra provider calls through the existing gates, but adds frequent full open-PR
   database scans and makes the configured interval mean two different things.
3. Wake the fast-refresh loop after notification ingestion. This improves
   notification-backed changes but does not cover self-authored activity and
   therefore cannot satisfy the policy by itself.

## Design

For PRs whose effective activity time is within the 30-minute hot window,
`activeMRDueForFastSync` returns true on every fast-refresh pass. The effective
activity time remains the newer of authoritative PR activity and linked
notification activity.

PRs outside the hot window retain the existing five-minute warm cadence based on
`detail_fetched_at`. PRs never fetched remain immediately eligible.

The fast-refresh loop remains configured at two minutes. Host-level eligibility,
rate-limit backoff, API budgets, and GitHub conditional detail requests remain
unchanged. Consequently, a hot PR makes at most one background refresh attempt
per scheduler pass; foreground refreshes no longer reset the background phase.

## Verification

Add a regression test showing that a hot PR whose detail was fetched less than
two minutes ago is still selected on the current scheduler pass. Preserve tests
showing that warm PRs are excluded until their five-minute interval elapses and
that notification activity can promote an otherwise stale PR into the hot lane.

Run the focused GitHub sync tests, the package test suite, the existing
full-stack Activity test, and the repository verification required for the final
change. Manual QA should post a comment immediately after a detail refresh and
confirm it appears on the next two-minute pass (allowing provider and request
execution time).
