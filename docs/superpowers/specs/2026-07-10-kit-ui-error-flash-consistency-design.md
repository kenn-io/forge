# Kit UI Error Flash Consistency Design

## Goal

Make transient operation failures use the application's shared kit-ui flash stack consistently, beginning with Kata and covering the rest of the frontend in the same pass.

## Error Presentation Rule

Classify an error by what the user needs to do next:

| Error kind | Presentation | Examples |
| --- | --- | --- |
| A user-triggered operation failed and the current surface remains usable | Shared kit-ui error flash | Create, rename, save, delete, unlink, refresh, copy, or state-change failure |
| The current surface cannot load or remains unavailable | Inline persistent state | Initial page, list, detail, diff, preview, or search-result load failure |
| The user must correct a particular input | Inline next to that input | Required value, invalid syntax, or rejected local form value |
| The application needs the user to resolve a durable conflict | Inline in the affected workflow | Stale edit, merge conflict, or recurrence conflict |
| The application is degraded but still operating | Inline status indicator | Daemon connection, sync, runtime, or provider health state |

Failures must appear on only one error surface. A path migrated to a flash removes its duplicate local error state and rendering.

## Architecture

Use the existing `@middleman/ui/stores/flash` re-export and the single shell-level kit-ui `FlashBanner`. Feature components report transient failures through `showFlash(message, { tone: "danger" })`; they do not mount additional banners or introduce feature-specific toast stores.

The shell mounts the banner in a page-level fixed layer: immediately below the global header in the full application and at the top edge in a headerless embedded presentation. Feature layouts, panes, modals, and scrolling containers must not position or contain the flash stack, so local scrolling cannot move or hide an application error.

Keep error normalization close to the API boundary that already understands the failure envelope. This change standardizes presentation, not backend error contracts or message wording.

## Application Pass

Audit Svelte components and rune stores under `frontend/src` and `packages/ui/src` for locally rendered errors. For each path, trace the error source and classify it using the table above rather than migrating based on CSS class names alone.

Kata's mutation coordinator, project creation and rename actions, workspace creation, and message unlinking are the first migration targets. Kata load failures, daemon health, validation, graph availability, and conflict states remain inline.

Apply the same classification to the other application modes and shared provider UI. Preserve inline errors whose placement communicates unavailable content or the exact input/workflow that needs attention.

## Accessibility and Interaction

The mounted kit-ui flash stack owns announcement, dismissal, stacking, and timing behavior. Migrated components must not add parallel live regions for the same failure.

An operation failure must leave the initiating surface usable: busy state clears, dialogs and editors stay open when retry is possible, and existing data remains visible.

## Testing

Add or update focused component and store tests for representative migrated paths. Tests should spy on the shared flash module and assert the normalized message and error tone, while confirming the former duplicate inline error is absent where relevant.

Retain coverage for inline validation, persistent load failures, conflicts, and degraded status. Browser shell coverage proves that shared-store flashes render at the page-level top edge in desktop, compact, and embedded presentations rather than inheriting a feature container's position or scroll behavior.

Run Svelte analysis on every edited component, kit-ui checks, affected focused tests, and the complete frontend test suite after the final frontend edit.

## Non-goals

- Replacing every inline error with a flash.
- Changing API error envelopes or backend error behavior.
- Adding a new application-specific notification abstraction.
- Rewriting success notifications or non-error informational messages unless needed to prevent duplicate presentation on an edited path.
