# Kata Ready Task Filter

## Goal

Add `Ready` to the Kata task list's status dropdown so maintainers can view
tasks that the selected Kata daemon considers ready to work.

## Scope

- Add `Ready` alongside `Open`, `Closed`, and `All` in the existing status
  control.
- Treat readiness as an authoritative Kata daemon result. Middleman must not
  derive readiness from task relationship fields.
- Support Ready in both all-project and project-scoped task lists.
- Continue applying the existing owner, label, and text-query controls to the
  authoritative ready result set.
- Persist and restore Ready through the existing Kata workspace filter state.

This change does not add a Ready sidebar system view, change Kata's readiness
rules, or add a Middleman backend API. Requests continue through the existing
Kata proxy.

## Data Model

Extend `KataTaskStatusFilter` from `"open" | "closed" | "all"` to include
`"ready"`. Ready is a list-filter mode, not a task status: returned tasks keep
their daemon-provided `status`, and UID membership alone determines whether a
row is Ready.

The existing `KataTaskSearchFilters` object remains the single filter contract
for UI state, persistence, store loading, and task API calls.

Every normalized search response carries `ready_issue_uids`. It contains the
complete daemon-returned UID set for Ready searches and is an empty array for
other statuses. The field is required rather than optional so a missing or
malformed membership cannot be mistaken for a valid empty Ready result.

## Data Flow

When `filters.status` is `"ready"`:

1. The search panel emits the updated filter object through its existing
   `onChange` callback.
2. The Kata workspace store performs a search load using the selected daemon.
3. For all-project scope, the task client requests `GET /api/v1/ready`.
4. For project scope, the client resolves the project UID to its numeric ID and
   requests `GET /api/v1/projects/{project_id}/ready`.
5. The client normalizes the response and captures its complete UID membership
   before applying presentation filters.
6. The client narrows returned rows by project, owner, label, and text query
   without narrowing the authoritative UID membership.
7. The workspace renders and persists the result through its existing search
   and filter flows.

Ready endpoint requests are not narrowed by owner, label, or text query because
the raw response remains authoritative for expandable descendant membership.
Client-side filtering narrows only the root result rows.

## UI Behavior

The status dropdown order is `Open`, `Ready`, `Closed`, `All`. Selecting Ready
updates the accessible control name to `Status: Ready` and otherwise preserves
the current search-toolbar layout.

Changing from Ready to another status immediately hides rows that do not match
the newly selected filter while the replacement request is pending, following
the existing status-change behavior. Selecting a ready task uses the ordinary
detail, relationship, graph, and mutation flows because readiness does not
change the task's underlying status or identity.

While Ready is active, every displayed or selectable root and expanded child
must belong to the latest daemon-returned Ready UID set; open status alone never
qualifies, and entering Ready clears stale membership until its request lands.

Ready hierarchy follows these acceptance rules:

| Daemon membership and relationship               | List result                                                                                       |
| ------------------------------------------------ | ------------------------------------------------------------------------------------------------- |
| Parent and child are ready                       | Preserve their parent-child hierarchy.                                                            |
| Parent is ready and child is not                 | Show the parent; omit the child when expanded.                                                    |
| Child is ready and parent is not                 | Promote the child to a root; do not show the parent.                                              |
| A routed ready target has any non-ready ancestor | Temporarily promote the target to a root; never reconnect ready nodes across an omitted ancestor. |

The latest non-superseded accepted Ready request is one authoritative snapshot;
an older request that finishes later is inert. Starting any Ready refresh clears
the previous membership before the request runs, including refreshes caused by
mutations or events. Duplicate UIDs collapse naturally in the membership set,
and a missing parent uses the child-as-root rule. Each accepted owner, label, or
status change downloads the complete Ready set; text input keeps the existing
debounce. No snapshot cache is added because cached membership would weaken
daemon freshness.

## Error Handling

Ready endpoint failures use the existing `KataTaskAPIError` parsing and Kata
workspace view-error presentation. A failed Ready request does not fall back to
locally computed readiness or to the Open list. Interactive and persisted-entry
failures retain the attempted Ready filter, expose retry, and render an empty
membership until a later authoritative response succeeds.

A successful task mutation remains committed if its following Ready refresh
fails. Authority loss clears list rows, selected detail, disclosure state, and
the selected issue URL; Retry refreshes membership and the list without
repeating the mutation or automatically restoring the cleared selection. A
failed persisted restoration also renders no selection, but keeps the stored
selection as retry input so a successful restoration Retry can revalidate it.
Event-driven refresh failures use the same authority-loss cleanup and Retry
path, including failures that happen before the Ready request itself and
clearing the persisted selection before route reconciliation. If automatic
stream replay later completes a current-scope Ready refresh, the recovered
membership clears the obsolete error and Retry without restoring selection.

A successful Ready response must contain an `issues` array whose entries carry
non-empty UIDs. Missing or malformed membership fails with
`invalid_ready_response`; it is never normalized to an authoritative empty set.

An unknown project UID returns an empty ready result, matching current
project-scoped search behavior.

## Implementation Order

1. Define strict Ready response normalization and mandatory UID membership.
2. Apply membership lifecycle rules in search, mutation, event, and restore flows.
3. Enforce membership in hierarchy, Logbook, selection, and route presentation.
4. Cover response validation and failure recovery in component and full-stack tests.

## Testing

- Search-panel component coverage verifies the Ready option and emitted filter.
- Task-client tests verify global and project Ready endpoint selection,
  normalization, daemon pinning, and narrowing by owner, label, query, and
  project scope.
- Store and workspace tests verify Ready triggers the search path, persists and
  restores correctly, invalidates membership before failed refreshes, and does
  not confuse ready-list membership with task `status`.
- List tests cover ready targets absent from the current flat result, including
  omitted immediate and intermediate ancestors, so route restoration cannot
  manufacture false hierarchy.
- Full-stack Kata tests cover authoritative endpoint failure and expanded-child
  selection across mutation refresh and persisted reload.
- The full frontend Vitest suite and Svelte checks run after the final edit.
