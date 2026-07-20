# Kata Workspace Snapshot Coordinator

## Goal

Replace the browser-owned Kata authority, refresh, and recovery paths with one
Middleman-owned snapshot coordinator. Middleman reads Kata through the published
generated Go client, caches snapshots briefly in memory with `ttlcache`, and
returns one atomic workspace snapshot to the UI.

The immediate user-visible deliverable remains the Ready task filter, but Ready
must use the same snapshot contract as Open, Closed, and All rather than adding
another special membership path.

## Forward-Only Boundary

This is a forward-only migration.

- Delete the frontend's direct Kata list, Ready, project, detail, and event
  composition paths as their replacements land.
- Do not keep a legacy proxy fallback, dual read path, compatibility adapter,
  deprecated response shape, or migration gate.
- The generic Kata passthrough proxy may remain for unrelated mutation routes,
  but workspace reads must not fall back to it.
- Middleman persists no Kata task, membership, snapshot, or cursor data to its
  database or filesystem.

## Upstream Client

Middleman imports `go.kenn.io/kata/pkg/client` and its generated types. Each
configured daemon gets a typed client built with `client.NewForTarget`, the
resolved daemon URL, `kataDaemonForwardToken`, and the daemon's
`AllowInsecure` setting.

The snapshot path uses generated methods for:

- global and project Ready results;
- global and project issue lists;
- project discovery;
- issue detail by UID;
- event polling and the live event stream.

Middleman maps generated Kata DTOs into its own stable snapshot response. The
frontend does not consume generated Kata types directly.

## Cache Model

`KataSnapshotCoordinator` owns one `ttlcache.Cache` keyed by an immutable query
key:

```go
type kataSnapshotKey struct {
    DaemonID  string
    View      string
    ProjectUID string
    Authority string
}
```

The cache stores only accepted, immutable snapshots. It does not store request
errors, retries, provisional state, selected routes, or browser persistence.

- Default TTL: five seconds.
- Capacity: bounded by `ttlcache` cost/entry limits configured for the expected
  number of daemons and active query keys.
- A cache hit returns the accepted snapshot immediately.
- A cache miss is singleflight-coalesced by query key so concurrent browser
  requests perform one daemon read.
- An accepted mutation or Kata event invalidates that daemon's cached snapshot
  keys. The next read repopulates them.
- TTL expiration is a freshness bound, not long-term storage. Restarting
  Middleman starts with an empty cache.

Presentation filters are deliberately absent from the cache key. Owner, label,
query, sorting, and hierarchy projection run against the accepted authority
snapshot without invalidating or refetching it.

## Minimal Kata Frontend Service API

Middleman exposes a deliberately small frontend-oriented API. It is not a
generated-client re-export and does not mirror the complete Kata daemon API.

The initial surface has two endpoints:

```text
GET /api/v1/kata/tasks/snapshot
X-Middleman-Kata-Daemon: <daemon id>

GET /api/v1/kata/tasks/events
X-Middleman-Kata-Daemon: <daemon id>
Accept: text/event-stream
```

The snapshot query parameters select `view`, `project_uid`, `status`, and an optional
`selected_issue_uid`. The response is:

```ts
interface KataWorkspaceSnapshotResponse {
  daemon_id: string;
  key: {
    view: KataTaskViewName;
    scope: KataTaskSearchScope;
    authority: "open" | "ready" | "closed" | "all";
  };
  generation: number;
  event_cursor: number;
  fetched_at: string;
  member_issue_uids: string[];
  issues: KataTaskSummary[];
  selected_detail?: KataTaskDetail;
}
```

The frontend event stream emits only invalidation frames containing daemon ID
and the coordinator generation/high-water cursor. It does not forward raw Kata
event payloads and the browser never patches task state from an event. On an
invalidation frame, the browser requests the current snapshot intent; TTL and
singleflight prevent duplicate daemon work.

Existing mutation endpoints remain outside this frontend read service. They
invalidate the affected daemon's snapshot cache after an accepted mutation.

`member_issue_uids` is authoritative membership before owner, label, or text
projection. It exists for every authority mode. Selection validity is checked
against this set, never against the currently projected root rows.

The snapshot generation is process-local and monotonically increases whenever
the coordinator accepts a newly fetched snapshot for a key. It prevents late
responses from replacing newer browser state; it is not persisted.

## Middleman Coordinator

`KataSnapshotCoordinator` is the sole writer for workspace read state.

1. Resolve the requested daemon and obtain its generated client.
2. Build the authority key from daemon, view, scope, and status.
3. Serve an unexpired cached snapshot when present.
4. Coalesce a cache miss with other requests for the same key.
5. Read the complete authority set through the generated client.
6. Normalize all tasks by full UID and preserve generated relationship fields.
7. Optionally read selected detail only when its UID belongs to membership.
8. Read or advance the daemon event cursor as part of the same serialized
   coordinator operation.
9. Publish one immutable snapshot to the TTL cache and return it.

Events are invalidation signals. `KataSnapshotCoordinator` consumes the
generated Kata event stream, batches events, invalidates the affected daemon
once, and broadcasts one frontend invalidation frame. A batch of events causes
one subsequent snapshot refresh regardless of event count.

## Browser State

The frontend replaces independent `currentView`, `readyIssueUIDs`, retry
closures, and event-refresh booleans with one discriminated state:

```ts
type KataWorkspaceState =
  | { phase: "accepted"; snapshot: KataWorkspaceSnapshot }
  | { phase: "loading"; previous: KataWorkspaceSnapshot | null; intent: KataSnapshotIntent }
  | { phase: "degraded"; snapshot: KataWorkspaceSnapshot | null; intent: KataSnapshotIntent; error: string }
  | { phase: "switching"; previous: KataWorkspaceSnapshot; daemonID: string }
  | { phase: "terminal"; snapshot: KataWorkspaceSnapshot | null; error: string };
```

Routes and local workspace preferences remain browser concerns, but they are
written only after an accepted snapshot transition. Retry is derived from the
failed intent in `degraded`; it is not stored as a closure.

Owner, label, text query, sorting, and hierarchy are pure projections over the
accepted snapshot. Changing them does not clear membership, refetch Ready, or
block unrelated actions.

## List, Detail, and Graph Ownership

`KataIssueList` becomes a pure renderer:

- no direct `api.issue` calls;
- no component-local authoritative child cache;
- no temporary membership exceptions;
- children and reveal paths are projected from the accepted snapshot entity
  set and membership.

Detail and graph enrichment go through the same Middleman coordinator. Full UID
is the only identity key. A sparse payload cannot overwrite fields present in a
newer or richer generated payload.

## Error Semantics

Starting a request does not mutate the accepted snapshot.

- Success atomically commits key, membership, issues, detail, cursor, and
  generation.
- Failure while changing authority enters `degraded` for the attempted key.
- Failure after event invalidation does not display invalidated membership.
- A projection-only change cannot produce an authority failure because it
  performs no network request.
- Retry repeats the recorded snapshot intent and is automatically removed by
  the next accepted transition.
- A routed UID absent from `member_issue_uids` is canonicalized out before any
  detail request.

## Testing

- Go unit tests cover query-key construction, five-second TTL reuse,
  singleflight coalescing, daemon invalidation, generated-client error mapping,
  and membership-gated detail reads.
- Go HTTP tests exercise the snapshot endpoint against a real fake Kata HTTP
  server through the generated client.
- Frontend store tests cover atomic snapshot acceptance, stale-generation
  rejection, projection-only filters, routed selection membership, and derived
  retry state.
- Full-stack Playwright coverage verifies global/project Ready, valid hidden
  Ready child restoration, invalid routed selection removal, event
  invalidation, and recovery through ordinary navigation.
- Tests assert Middleman's logic and integration seams, not generated-client or
  `ttlcache` library behavior.

## Non-Goals

- Persisting Kata state in Middleman's database or filesystem.
- Supporting old and new workspace read paths simultaneously.
- Adding a TTL configuration UI or cache administration endpoint.
- Mirroring the generated Kata API as a large Middleman frontend API.
- Reimplementing Kata readiness or relationship semantics.
- Adding compatibility fallbacks for older code introduced within this PR.
