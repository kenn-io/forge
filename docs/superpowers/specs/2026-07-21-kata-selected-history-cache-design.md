# Kata selected-history and enrichment cache design

## Problem

The Kata frontend service currently reconstructs a selected issue's history by
polling the cross-project `/api/v1/events` stream from cursor zero. It stops
after four pages or 4,000 scanned events and returns at most 100 matching
events. Newer issues routinely occur after that prefix, so selection returns a
history enrichment warning even though Kata is healthy. Against a remote
daemon, the same scan adds several sequential network requests and large
cross-project payloads to every selection.

The base authority snapshot is cached for five seconds, but selected detail,
history, and graph enrichment are not. Re-selecting an issue therefore repeats
all daemon reads.

## Data flow

Global issue and event APIs remain the bootstrap and invalidation authority for
the workspace. They are not used to reconstruct selected issue history.

For an authorized selection, Middleman uses the generated Kata client to:

1. Call `ShowIssueByUID` for the selected issue's detail, comments, links,
   labels, hierarchy, and ETag.
2. Fully paginate `PollProjectEvents` for the selected issue's project and
   filter the complete result by issue UID.
3. Call the existing reachable-graph API only when a graph source is requested.
4. Resolve the Middleman workspace target locally.

There is no scan-page limit, scanned-event limit, or returned-history limit.
Cursor validation and cancellation remain required. Empty pages terminate
pagination.

## Caching

Middleman keeps only bounded, in-memory `ttlcache` entries. Every cache uses a
five-second non-touching TTL, so reads do not extend entry lifetime.

The existing authority cache remains keyed by daemon identity and authority
request. A daemon-read cache is added for enrichment inputs:

- issue detail: daemon epoch plus issue UID;
- complete project event stream: daemon epoch plus project ID;
- reachable graph: daemon epoch plus source issue UID and graph options.

Concurrent loads for the same key are coalesced. Selecting multiple issues in
one project can therefore reuse the same project event snapshot, which removes
the repeated remote scan while keeping the result ephemeral.

All daemon-read entries are invalidated when the coordinator invalidates that
daemon: an SSE invalidation, acknowledged mutation, daemon target rotation, or
daemon restart generation change. Cache capacity limits affect only reuse;
they never truncate API results or change correctness.

## Errors and authority

The selected issue must still be authorized by the accepted authority snapshot
or reachable graph before enrichment is loaded. Detail, history, graph, and
workspace-target failures remain independent enrichment errors.

The obsolete `selected task history scan limit exceeded` error and its UI
warning path are deleted. Malformed pagination, cursor reset, transport failure,
or cancellation continue to surface as history-unavailable errors without
discarding successfully loaded detail.

## Verification

Tests must prove:

- selected history uses `PollProjectEvents`, not `PollEvents`;
- pagination continues until an empty page and returns every matching event;
- unrelated project events are filtered without imposing a result cap;
- repeated selections reuse cached detail and project events within the TTL;
- different issues in one project share the cached project event stream;
- daemon invalidation forces fresh enrichment reads;
- the HTTP snapshot for a recent issue contains history without the scan-limit
  warning;
- the visible Kata selection workflow passes in Chromium and Firefox.

No compatibility adapter, fallback global scan, dual read, persisted Kata
state, or historical-document edit is part of this change.
