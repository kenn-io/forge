# Roborev Recovery Polling Design

## Problem

The Roborev daemon store performs one health request when the application
starts and then polls every 30 seconds. If the initial request lands during a
daemon restart, `/api/v1/roborev/status` can validly return HTTP 200 with
`available: false`. The Reviews view remains in its unavailable state until the
next 30-second poll, longer than the recovery test's 15-second assertion.

The view also opens the Roborev event stream while the daemon is known to be
unavailable. Its reconnect loop then produces expected proxy failures during
the outage, obscuring useful test output.

## Design

Change daemon polling to a self-scheduling timeout:

- probe every 1 second while unavailable;
- retain the existing 30-second interval while available;
- use a polling generation so an in-flight request cannot reschedule itself
  after `stopPolling` or a later `startPolling`;
- preserve the existing false-to-true `onRecover` behavior that reloads jobs
  and any selected review.

Make the Reviews view's event-stream effect conditional on daemon
availability. It will connect when health becomes available and disconnect
through effect cleanup when availability is lost or the view is destroyed.

Rewrite the status-strip recovery test so it explicitly stops the daemon,
loads the unavailable page, starts the daemon, and observes automatic recovery.
The test will no longer depend on state left by an earlier serial test.

## Validation

- Add a focused daemon-store test with fake timers for unavailable and healthy
  polling intervals.
- Run the full Vite+ unit suite.
- Run the complete Roborev Playwright suite because the shared full-stack spec
  changes.
- Run the repository frontend checks before publishing.

## Out of Scope

- Treating HTTP 200 alone as daemon readiness.
- Shortening the healthy polling interval.
- Changing Roborev's event-stream retry algorithm or proxy response contract.
- Fixing unrelated flaky tests in the same Playwright suite.
