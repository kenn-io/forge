# ADR 0005: Global Roborev Filter Persistence

Date: 2026-08-10

## Status

Accepted

## Context

The Roborev review list resets **Hide closed** and **Show auto-design** to their defaults whenever its jobs store is recreated. This makes maintainers repeatedly restore the same view after a reload or after moving between the main Reviews view and a workspace review sidebar.

These choices describe how one browser should present Roborev jobs. They do not describe a workspace, repository, branch, daemon, or server-side user setting.

## Decision

Persist both boolean filters as browser-global preferences owned by the Roborev jobs store. Use the distinct `localStorage` keys `kenn-forge:roborev:hideClosed` and `kenn-forge:roborev:showAutoDesign`, with `"1"` for enabled and `"0"` for disabled.

Every newly created Roborev jobs store restores the same two preferences, including stores used by workspace review sidebars. Existing stores retain their reactive values across navigation. Missing, unavailable, or unrecognized storage retains the current defaults: closed reviews remain visible and auto-design jobs remain hidden.

Every mutation path writes the preference before refreshing the jobs query, so filter-bar actions and keyboard shortcuts behave consistently. Storage access is best-effort: read or write failures do not block in-memory filtering, request refreshes, or surface application errors.

Repository, branch, status, search, job type, and sort state remain transient.

## Consequences

- The two choices follow the browser rather than a workspace or daemon.
- Newly mounted main and sidebar review surfaces start with consistent filters.
- Storage remains local to one browser and does not follow a user to another device.
- Focused jobs-store tests must cover defaults, writes, restoration in a new store, and the resulting request query.
