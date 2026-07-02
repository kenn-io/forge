---
name: small-change-verification
description: Use when making small or localized changes that could still affect user-visible behavior, API contracts, database queries, test runtime, CI, generated artifacts, or cross-layer data flow.
---

# Small Change Verification

## Core Idea

Small changes fail when their real blast radius is misread. Before editing, name the kind of change and the most likely place a regression would be noticed.

## When to Use

Use for modest follow-up work such as:

- fixing a focused UI behavior;
- adjusting a DB query, filter, sort, or search condition;
- changing Huma routes, response types, generated clients, or API errors;
- touching Playwright, CI, test scripts, or runtime config;
- updating provider-visible behavior without adding a whole provider.

Small means scoped, not exempt from repository requirements. Do not use this for large feature design; use a planning or docs-grilling workflow first.

## Classify First

Pick the smallest matching class:

| Class | Typical files | Main regression question |
| --- | --- | --- |
| `ui-only` | Svelte components, stores, CSS | What visible workflow or persisted UI state could break? |
| `api-contract` | Huma routes, API types, generated clients | What client contract or generated artifact changes? |
| `db-query` | queries, migrations, filters | What persisted or sorted result would the user notice? |
| `test-runtime` | Playwright, CI, scripts, Makefile | Does local execution match the failing or intended runtime? |
| `provider-visible` | platform clients, capabilities, routes | Which provider, host, or capability boundary is affected? |

Write one sentence before editing:

```text
This is a <class> change; the likely regression surface is <observable behavior>.
```

If multiple classes apply, use the strictest relevant checks.

## Minimum Checks

| Class | Minimum verification |
| --- | --- |
| `ui-only` | Relevant component/store test; affected Playwright or full-stack e2e when the change alters a visible workflow. |
| `api-contract` | `make api-generate`, review checked-in OpenAPI/client diffs, and run the narrow Go/API test that consumes the contract. |
| `db-query` | Query/unit test with literal expected rows, plus server/API test when HTTP output changes. |
| `test-runtime` | Re-run the exact affected command in the same runtime shape: container, browser, env var, or CI script path. |
| `provider-visible` | Provider/package test plus server/API or UI capability test at the boundary users see. |

If one class points at another, run both checks. Example: a DB-backed search fix is usually `db-query` and API-visible.

## Done When

The classification sentence is written, applicable checks have run, skipped checks are justified, generated diffs are reviewed when present, and failures are investigated before completion.

## Context Map

- API, DB, provider, and e2e boundaries: `context/testing.md`
- UI state, route identity, persistence, and interaction behavior: `context/ui-interaction-contracts.md`
- Provider identity, host scoping, and capability rules: `context/platform-sync-invariants.md`
- Provider package layout and route helpers: `context/provider-architecture.md`

## Common Mistakes

- Calling a change "just config" when it changes CI runtime or browser behavior.
- Testing only the narrow helper when the user sees the behavior through HTTP or UI.
- Updating generated files without reviewing whether the public contract should have changed.
- Adding a Playwright assertion for visibility but skipping the component/store state that caused it.
