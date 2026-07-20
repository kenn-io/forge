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
their daemon-provided `status`, normally `"open"`.

The existing `KataTaskSearchFilters` object remains the single filter contract
for UI state, persistence, store loading, and task API calls.

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

## Error Handling

Ready endpoint failures use the existing `KataTaskAPIError` parsing and Kata
workspace view-error presentation. A failed Ready request does not fall back to
locally computed readiness or to the Open list.

An unknown project UID returns an empty ready result, matching current
project-scoped search behavior.

## Testing

- Search-panel component coverage verifies the Ready option and emitted filter.
- Task-client tests verify global and project Ready endpoint selection,
  normalization, daemon pinning, and narrowing by owner, label, query, and
  project scope.
- Store and workspace tests verify Ready triggers the search path, persists and
  restores correctly, and does not confuse ready-list membership with task
  `status`.
- The full frontend Vitest suite and Svelte checks run after the final edit.
