# ADR 0004: Explicit repository UI visibility

Date: 2026-08-07

## Status

Accepted

## Context

Provider-archived repositories must remain configured so kenn-forge can
discover, hydrate, and report their historical activity. Configured
repositories also feed the interactive repository catalog, so archive-only
projects appear in repository selectors and workspace creation even when
maintainers no longer use them for active work.

Provider archive state is not itself a presentation preference. An archived
repository may remain useful in the interactive UI, and a live repository may
need to be hidden without changing its sync or archive lifecycle.

## Decision

Give each configured repository an explicit `hide_from_ui` setting that
defaults to false. Settings remains the unfiltered management surface and
provides the visibility control. Each repository row exposes a compact gear
menu with a one-click `Hide from UI` or `Show in UI` action reflecting the
current state. Exact-repository menus also own the existing local-clone
configuration entry; glob menus expose visibility only. Visibility does not
add an inline checkbox, badge, or expanded row. The shared interactive
repository catalog omits a tracked repository when any matching configured
entry is hidden.

The visibility action saves immediately and closes the menu. The gear remains
available while the request is pending, but a reopened menu disables the
visibility item until the request settles. Save failures use the existing
flash banner and leave the server-confirmed visibility unchanged. Embedded
mode keeps the menu inspectable while disabling its configuration items.

Repository matching preserves provider, host, configured-path, rename
provenance, and glob semantics. Hiding a glob hides all of its matches.
Individually hiding a match of a visible glob requires an exact hidden entry;
showing a subset of a hidden glob requires unhiding the glob. One hidden entry
wins when visible and hidden entries overlap.

Visibility changes presentation only. Hidden repositories remain tracked and
available to sync, historical archive work, reports, and provider-aware direct
routes. The setting lives in operator configuration rather than the repository
catalog database because it is not provider-owned identity or metadata.

## Consequences

The global repository selector, workspace picker, and future consumers of the
interactive catalog consistently exclude hidden repositories. Settings still
shows those repositories so maintainers can restore visibility, refresh or
remove them, and manage local clone configuration. Repository-row tests cover
both action labels, the pending and embedded states, successful persistence,
and failure recovery without adding permanent row chrome.

No database migration or provider-specific behavior is required. Config save
and API contracts carry the explicit boolean, and selector normalization
clears a repository that becomes unavailable after a visibility change.
