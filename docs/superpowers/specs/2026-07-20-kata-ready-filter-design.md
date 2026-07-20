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
configured daemon gets a typed client that reuses Middleman's resolved daemon
transport and forwarded bearer token. The convenience client embeds the
generated client and also provides the non-buffering `StreamEventsRaw` method;
the generated buffered stream method is not used for a live SSE connection.

The snapshot path uses generated methods for:

- global and project Ready results;
- global and project issue lists;
- project discovery;
- issue detail by UID;
- selected issue history and reachable graph enrichment;
- event polling and the live event stream.

Middleman maps generated Kata DTOs into its own stable snapshot response. The
frontend does not consume generated Kata types directly.

## Cache Model

`KataSnapshotCoordinator` owns one `ttlcache.Cache` keyed by an immutable query
key:

```go
type kataSnapshotKey struct {
    DaemonID   string
    View       string
    Scope      string
    ProjectUID string
    Authority  string
}
```

The cache stores only accepted, immutable snapshots. It does not store request
errors, retries, provisional state, selected routes, or browser persistence.

- Default TTL: five seconds.
- Capacity: 128 entries, with access not extending the five-second TTL.
- Expiry, deletion, and capacity eviction remove keys from the daemon
  invalidation index. The cleanup worker is tied to Middleman's background
  context and stops during server shutdown.
- A cache hit returns the accepted snapshot immediately.
- A cache miss is singleflight-coalesced by query key plus captured daemon epoch
  so concurrent browser requests perform one daemon read without joining a
  pre-invalidation flight.
- An accepted mutation or Kata event increments that daemon's invalidation
  epoch and removes its cached authority keys. In-flight loads capture the
  epoch and may publish only when it is unchanged; otherwise their result is
  discarded and loaded again under the new epoch.
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

The snapshot query parameters select `view`, optional `project_uid`, `status`,
and optional request-specific enrichment: `selected_issue_uid`, selected
history, and reachable graph depth/hide-done. Selection and graph parameters do
not enter the authority cache key. The response is:

```ts
interface KataWorkspaceSnapshotResponse {
  server_instance_id: string;
  daemon_id: string;
  key: {
    view: KataTaskViewName;
    scope: { kind: "global" } | { kind: "project"; project_uid: string };
    authority: "open" | "ready" | "closed" | "all";
  };
  generation: number;
  invalidation_epoch: number;
  event_cursor: number;
  fetched_at: string;
  projects: KataProjectSummary[];
  member_issue_uids: string[];
  issues: KataTaskSummary[];
  selected_detail?: KataTaskDetail;
  selected_history?: KataTaskEvent[];
  selected_graph?: KataReachableGraphResponse;
}
```

The frontend event stream emits only invalidation frames containing server
instance ID, daemon ID, invalidation epoch, and the Middleman frontend-stream
cursor. It does not forward raw Kata event payloads and the browser never
patches task state from an event. On an invalidation frame, the browser requests
the current snapshot intent; TTL and singleflight prevent duplicate daemon
work.

Existing mutation endpoints remain outside this frontend read service. They
invalidate the affected daemon's snapshot cache after an accepted mutation.

`projects` is the complete catalog required for project navigation, including
empty projects. `member_issue_uids` is authoritative membership before owner,
label, or text projection. It exists for every authority mode. Selection
validity is checked against this set, never against the currently projected
root rows.

The snapshot generation is process-local and monotonically increases whenever
the coordinator accepts a newly fetched authority snapshot. It is not a browser
navigation token. The browser owns a local request-intent sequence and accepts
a response only while that request still owns the current canonical intent.
Generation comparisons apply only within the same `server_instance_id` and
canonical key; a new server instance or a deliberate switch to another cached
key is accepted regardless of numeric generation.

## Middleman Coordinator

`KataSnapshotCoordinator` is the sole writer for workspace read state.

1. Resolve the requested daemon and obtain its generated client.
2. Canonicalize one authority request and build its key from daemon, view,
   global/project scope, project UID, and status. Empty project UID is valid
   only for global scope; unknown projects are explicit errors.
3. Serve an unexpired cached snapshot when present.
4. Capture the daemon invalidation epoch and coalesce a cache miss by key and
   epoch.
5. Read the complete project catalog and authority set through the generated
   client.
6. Normalize all tasks by full UID and preserve generated relationship fields.
7. Before publishing, verify the daemon epoch is unchanged. Discard and retry a
   stale in-flight result rather than repopulating invalidated data.
8. Publish only the authority snapshot to the TTL cache with a new generation.
9. Membership-check the requested selected UID, then attach detail,
   Middleman-resolved `workspace_target`, selected history, and optional graph
   enrichment after the cache lookup. Request-specific enrichment is never
   cached as another authority snapshot.
10. Return the current per-daemon Middleman frontend-stream cursor so a browser
    can subscribe without a snapshot-to-stream race.

Events are invalidation signals. One per-daemon supervisor owns Kata polling,
the non-buffering generated-client stream, upstream cursor recovery, and a
bounded Middleman `EventHub` for browser fan-out. It coalesces event bursts,
increments the daemon epoch once, invalidates the daemon cache, and broadcasts
one small frontend frame. Kata event payloads and upstream cursors remain
server-internal; a stale or reset browser cursor produces a compact invalidation
frame that causes one snapshot refresh.

## Browser State

The frontend replaces independent `currentView`, `readyIssueUIDs`, retry
closures, and event-refresh booleans with one discriminated state:

```ts
type KataWorkspaceState =
  | { phase: "accepted"; snapshot: KataWorkspaceSnapshot }
  | { phase: "loading"; previous: KataWorkspaceSnapshot | null; intent: KataSnapshotIntent }
  | { phase: "degraded"; intent: KataSnapshotIntent; error: string }
  | { phase: "switching"; previous: KataWorkspaceSnapshot; daemonID: string }
  | { phase: "terminal"; error: string };
```

Authority intent and presentation state are separate. Daemon, view,
global/project scope, status, and selected route form the authority intent.
Owner, label, text query, sorting, hierarchy expansion, and graph display
preferences are browser presentation state and persist immediately without an
authority request. A late authority response never rewinds newer presentation
choices. Retry is derived from the failed authority intent in `degraded`; it is
not stored as a closure.

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

Detail, selected history, and graph enrichment go through the same Middleman
snapshot endpoint after membership validation. `selected_detail` retains the
existing Middleman `workspace_target` enrichment. Graph data contains the
source UID, normalized nodes, typed edges, unresolved references, depth, and
hide-done result; graph nodes may extend beyond current authority membership.
Full UID is the only identity key. Recurrence CRUD is not workspace authority
and remains outside this service.

## Error Semantics

Starting a request does not mutate the accepted snapshot.

- Success atomically commits server instance, key, epoch, projects, membership,
  issues, selected enrichment, cursor, and generation.
- Failure while changing authority enters `degraded` for the attempted key and
  does not present the previous key's rows as though they satisfied the failed
  intent.
- Event invalidation immediately makes the accepted snapshot non-actionable.
  If refresh fails, invalidated rows and membership are not displayed and the
  selected route is canonicalized away.
- A projection-only change cannot produce an authority failure because it
  performs no network request.
- Retry repeats the recorded snapshot intent and is automatically removed by
  the next accepted transition.
- A routed UID absent from `member_issue_uids` is canonicalized out before any
  detail request.

## Testing

- Go unit tests cover canonical key construction, five-second TTL reuse,
  capacity/index cleanup, epoch-fenced singleflight, daemon invalidation,
  generated-client error mapping, and membership-gated detail/history/graph
  enrichment.
- Go HTTP tests exercise the snapshot endpoint against a real fake Kata HTTP
  server through the generated client.
- Frontend store tests cover atomic snapshot acceptance, local request-intent
  ownership, restart/key-scoped generation comparison, projection-only filters,
  routed selection membership, invalidation failure, and derived retry state.
- Full-stack Playwright coverage verifies global/project Ready, empty project
  navigation, valid hidden Ready child restoration, selected workspace/graph
  enrichment, invalid routed selection removal, mutation and event invalidation,
  restart generation reset, and recovery through ordinary navigation.
- Tests assert Middleman's logic and integration seams, not generated-client or
  `ttlcache` library behavior.

## Non-Goals

- Persisting Kata state in Middleman's database or filesystem.
- Supporting old and new workspace read paths simultaneously.
- Adding a TTL configuration UI or cache administration endpoint.
- Mirroring the generated Kata API as a large Middleman frontend API.
- Reimplementing Kata readiness or relationship semantics.
- Folding recurrence CRUD into the workspace snapshot service.
- Adding compatibility fallbacks for older code introduced within this PR.
