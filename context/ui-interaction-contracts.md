# UI Interaction Contracts

Use this document for frontend behavior changes where the risk is not visual
style but stale identity, broken persistence, or surprising interaction
semantics.

## Purpose

- Make behavior-level UI contracts explicit.
- Keep route identity, persisted browser state, and keyboard/pointer semantics
  consistent across the app.
- Prevent narrow regressions that usually show up only after review or in e2e
  flows.

## Identity And Route State

Interactive surfaces must agree on which item is selected.

- Treat `platform_host` as part of PR and issue identity in route state, drawer
  state, and stale-detail guards.
- When host is omitted for the default GitHub host, normalize comparisons so
  `github.com` and an omitted host do not look like different items.
- Use shared named route/item reference types from
  `frontend/src/lib/stores/router.svelte.ts` instead of repeating anonymous
  `{ owner, name, number }`-style shapes.
- When a view changes from item A to item B, reset transient action state that
  could otherwise submit or render against the wrong item.

Responsive layout changes must not change route identity.

- Resizing a canonical PR or issue route must not rewrite `/pulls/...`,
  `/pulls/.../files`, `/issues/...`, or `/host/{platform_host}/...` into
  `/focus/...` or `/m/...`.
- Responsive presentation decisions belong in the shell/rendering layer. Route
  builders still follow the active route family: canonical builders for
  canonical routes, focus builders for explicit `/focus` routes, and mobile
  builders for explicit `/m` flows.
- If a canonical list route renders with the focus presentation because the
  viewport is compact, selecting an item should still navigate to a canonical
  detail route.
- Distinguish compact desktop presentation from phone-like presentation in
  state names and tests. Compact desktop may hide sidebars or use the focus
  presentation; phone-like contexts may additionally use mobile typography,
  touch hit targets, and phone-specific action layouts.

Examples of transient state that should usually reset on identity change:

- inline edit drafts
- merge/close/reopen dialogs
- approve/review forms
- embedded detail-tab selection when the parent surface owns the item

## Persistence Scope

Persisted controls must state their scope clearly.

- Browser-local preferences belong in `localStorage` only when the behavior is
  intentionally per-browser and not worth server settings.
- URL query state belongs in the route only when deep-linking or back/forward
  navigation is part of the feature contract.
- Server-backed settings belong in the API only when the preference should
  follow the user/config rather than one browser session.

Whenever a control persists, document and test:

- where it persists
- whether it is global, per-view, or per-item
- what happens after navigating away and back

## Keyboard Scope Precedence

Keyboard handlers must have one clear owner for each key press.

- Input fields, textareas, contenteditable elements, and terminal surfaces own
  printable keys while focused. Global shortcuts must not reinterpret those
  keystrokes.
- Modal frames outrank page-level shortcuts. When a modal, drawer, popover, or
  command surface is active, route and list navigation should run only through
  actions explicitly registered for that active surface.
- If two surfaces can expose the same binding, document the precedence in the
  action registration rather than relying on registration order.
- Shortcut labels and cheatsheet entries must match the actual key event
  contract, including required modifiers.
- Async shortcut handlers should report failures through the same user-visible
  error path as pointer-triggered actions, and must not leave the action marked
  in-flight forever.

## Modal Ownership

Any surface that blocks background interaction must own both focus and
shortcuts while it is open.

- Opening a modal-like surface should push a frame before focus moves inside
  the surface; closing it should pop only that frame.
- Close behavior must be local to the active surface. Escape should not also
  close a parent drawer or trigger route navigation unless the child declined
  the key.
- Background actions that are still visible should be disabled or skipped when
  their `when` predicate no longer matches the active modal state.
- Outside-click, focus-leave, and Escape close paths should converge on the same
  cleanup so stale frames, listeners, and highlighted rows are not left behind.
- Custom focus traps must cycle controls in rendered DOM order. If the trap
  builds the focusable list from a mixed selector list (`button, input, select,
  ...`), normalize the result by document position before wrapping Tab /
  Shift+Tab so selector-engine grouping cannot change keyboard order.

## Palette Persistence

Command palette state is browser-local unless a feature explicitly needs a
shareable URL or server-backed preference.

- Recent commands should store stable action references, not route-specific
  labels that become invalid after navigation.
- Stored recents must tolerate malformed JSON, unknown actions, and stale item
  references by pruning or ignoring bad entries without blocking the palette.
- Palette search, highlighted row, preview content, and command enablement
  should be derived from the current route context each time the palette opens.
- When palette content can overflow, keyboard navigation must scroll the
  highlighted result into view without moving focus out of the search field.

## Mobile Route Constraints

Mobile layouts may redirect between list and detail surfaces, but must preserve
the user's current item identity and deep link.

- Redirects should keep `platform_host`, owner, repo, number, and item kind
  together. Repo labels alone are ambiguous in multi-provider views.
- Desktop-only layout specs should opt out of mobile redirects explicitly so
  viewport changes do not make assertions pass against the wrong surface.
- Mobile detail routes should reset transient action state when switching items,
  the same way desktop split-detail routes do.
- Any mobile-specific back/forward behavior should be tested with direct links
  and with in-app navigation, not only from the default landing route.

## Nested Interaction Rules

Rows that contain buttons, links, or toggles need clear event ownership.

- Activating a nested control inside a clickable row must not also trigger the
  row's navigation or selection behavior.
- Escape should close drawers, split-detail panels, menus, or modals when that
  surface is currently active.
- Focus-visible states matter for controls that are visually subtle, such as tab
  close buttons or compact action affordances.
- If a component claims menu-like behavior, it must honor the keyboard and focus
  contract of that role. Otherwise, use simpler semantics honestly.

## Controlled Form Controls

A native form control driven from app state must not also run its own default
action, or the two fight and the control renders the inverse of its real state.

- A native `<input type="checkbox">` with `checked={state}` still toggles itself
  on a real click (the click default action). When app state owns the value,
  cancel that default action (`onclick` `preventDefault`) so the box only ever
  reflects `state`. The repo selector's tri-state checkbox
  (`frontend/src/lib/components/TreeCheckbox.svelte`) is the reference: a
  controlled custom control that suppresses the native toggle and drives
  selection from `onmousedown`, not click.
- Keep an underlying real `<input>` for accessibility and tests even when the
  visuals are custom: set `indeterminate` imperatively for the partial state, and
  expose `aria-checked` (`"mixed"` for partial) on the owning row.
- Tri-state selection that cascades to descendants must keep parent and child
  visuals consistent: a parent is checked only when all leaves are, partial when
  some are, unchecked when none are. A parent disagreeing with all its children
  is a desync bug.

## Filtering And Visibility Rules

Not every visibility control means "remove this entity entirely."

- Controls that toggle detail visibility should preserve the parent row unless
  the feature explicitly removes that category from the result set.
- When two data sources race, prefer the source that matches the user's current
  filter/scope rather than a stale but faster preview.
- Empty states should make it clear when filters, not missing data, are hiding
  results.

## Threaded Comments

Threaded comment rendering must preserve both timeline recency and reply
context.

- In reverse-chronological timelines, a thread is positioned where its newest
  visible event would have appeared.
- Inside that thread, render the main/root comment first, then threaded replies
  underneath in reverse-chronological order: newest reply, then the reply before
  that, and so on.
- Do not flatten same-`thread_id` comments into separate top-level timeline
  items when the surrounding UI is meant to show comment conversations.
- This contract should also guide future diff-comment UI: inline diff threads
  can anchor to a file/line position, but their compact timeline summaries
  should still use root-comment context plus newest-first replies.

## Optional Metadata Controls

Optional metadata must not reserve empty rows or placeholders when absent. Put
compact edit controls beside the metadata's normal display location, and keep
empty states for places where missing data itself is useful information.

Async detail mutations must be scoped to the currently visible item. Compare the
full provider route identity before opening transient UI or applying mutation
responses, and discard stale responses instead of patching another item.

## Testing Expectations

Behavior contracts should usually be tested where the user would notice the
breakage.

- Component tests for local state transitions, event propagation, and route/item
  identity helpers.
- Store tests for persistence scope and normalization logic.
- Playwright/e2e tests for navigation away/back, Escape behavior, nested button
  activation, and other multi-surface flows.
- For controlled native form controls, assert behavior under a real
  `fireEvent.click`, not only `fireEvent.mouseDown`. A mousedown-only test skips
  the native default action (e.g. a checkbox's own toggle) and will pass while a
  real click desyncs the control. A real-browser visual check catches this class
  of bug when the suite is green.
- Keyboard e2e tests should cover conflicting scopes, modal frame ownership,
  async action failure, overflow scroll-into-view, and mobile redirect cases
  when those behaviors are part of the feature.

Related docs:

- [`context/ui-design-system.md`](./ui-design-system.md) for visual primitives
  and styling guidance.
- [`context/notifications-in-activity.md`](./notifications-in-activity.md) for
  notification feed rows, state, and sync behavior.
- [`context/workspace-runtime-lifecycle.md`](./workspace-runtime-lifecycle.md)
  for runtime-specific workspace tab and shell behavior.
