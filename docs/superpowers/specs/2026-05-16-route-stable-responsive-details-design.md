# Route-Stable Responsive Details Design

## Goal

Make PR and issue detail pages responsive without changing the user's current URL when the viewport changes.

The current behavior surprises desktop users who open a canonical detail URL such as `/pulls/github/acme/widgets/1/files`, resize the app narrow enough to trigger the phone layout, and then resize it wide again. The app rewrites the URL to a mobile/focus route, so the original desktop route cannot naturally return when the viewport becomes wide again.

The desired behavior is simpler:

- The route expresses what the user is looking at.
- The viewport controls how that route is presented.
- Narrow canonical PR and issue routes render the same focus presentation used by `/focus`.
- Resizing the window never changes the route by itself.

## Current Behavior

`frontend/src/App.svelte` computes a phone-like layout from `window.innerWidth`, the app container size, and coarse pointer detection. When the app is on `activity`, `pulls`, or `issues`, `redirectPhoneToMobileRoute()` calls `replaceUrl()` if the viewport is phone-like.

That means:

- `/pulls` becomes `/m/pulls`.
- `/issues` becomes `/m/issues`.
- `/pulls/{provider}/{owner}/{name}/{number}` becomes `/focus/pulls/{provider}/{owner}/{name}/{number}`.
- `/pulls/{provider}/{owner}/{name}/{number}/files` becomes `/focus/pulls/{provider}/{owner}/{name}/{number}/files`.
- `/issues/{provider}/{owner}/{name}/{number}` becomes `/focus/issues/{provider}/{owner}/{name}/{number}`.

This loses source-route information. Once the URL changes to `/focus/...`, resizing wider cannot know whether the user originally came from a desktop route, a phone activity card, a copied focused link, or an embed-like focused flow.

## Design Decision

Remove automatic responsive route rewriting.

Canonical routes such as `/pulls`, `/pulls/...`, `/pulls/.../files`, `/issues`, and `/issues/...` remain the active location across all viewport widths. The app renders a desktop presentation at wide widths and the focus presentation at narrow widths, but the path does not change unless the user explicitly navigates.

Existing explicit mobile/focus routes can continue to exist as routable product surfaces, but they are no longer the mechanism for responsive adaptation from canonical desktop routes. They are entered only through explicit links, direct visits, or phone-first flows that intentionally target them.

`/focus` is acceptable only if it is a route over the same reusable focus presentation used by canonical PR and issue routes at narrow widths. It should not own a separate detail implementation that canonical routes cannot enter or leave as the viewport changes.

## Route Model

Routes keep their semantic meaning:

- `/pulls`: PR list route.
- `/pulls/{ref}`: PR detail route.
- `/pulls/{ref}/files`: PR files route.
- `/issues`: issue list route.
- `/issues/{ref}`: issue detail route.
- `/host/{platform_host}/pulls/{ref}`: self-hosted PR detail route.
- `/host/{platform_host}/pulls/{ref}/files`: self-hosted PR files route.
- `/host/{platform_host}/issues/{ref}`: self-hosted issue detail route.
- `/m/...`: explicit mobile shell routes.
- `/focus/...`: explicit focused routes.

Viewport width chooses presentation, not route identity:

| Route | Wide presentation | Narrow presentation |
| --- | --- | --- |
| `/pulls` | Desktop PR list with sidebar/detail placeholder | Focus PR list presentation |
| `/pulls/{ref}` | Desktop PR list + detail pane | Focus PR detail presentation |
| `/pulls/{ref}/files` | Desktop PR files detail | Focus PR files presentation |
| `/issues` | Desktop issue list with sidebar/detail placeholder | Focus issue list presentation |
| `/issues/{ref}` | Desktop issue list + detail pane | Focus issue detail presentation |
| `/host/{platform_host}/pulls/{ref}` | Desktop PR list + detail pane | Focus PR detail presentation |
| `/host/{platform_host}/pulls/{ref}/files` | Desktop PR files detail | Focus PR files presentation |
| `/host/{platform_host}/issues/{ref}` | Desktop issue list + detail pane | Focus issue detail presentation |

The browser location must remain byte-for-byte stable during viewport-only transitions, including query parameters and hash fragments.

The `{ref}` placeholder means the existing provider-aware identity tuple: `(provider, platform_host, owner, name, number)`, with `repoPath` preserving nested owner/group paths. Self-hosted routes must follow the existing `/host/{platform_host}/...` shape and must not be normalized back to default-host URLs during responsive presentation changes.

## Presentation Model

Introduce a small presentation decision in the app shell rather than using navigation as the responsive mechanism.

The app shell already has enough information to decide:

- Current route from `getRoute()`.
- Current page from `getPage()`.
- Phone-like layout from the existing viewport/container checks.
- Selected PR or issue from the route and stores.

The implementation should derive booleans along these lines:

- `isResponsivePhoneLayout`: true for phone-like width or the existing force-mobile testing flag.
- `isCanonicalPullDetailRoute`: true for `/pulls/{ref}` and `/pulls/{ref}/files`.
- `isCanonicalIssueDetailRoute`: true for `/issues/{ref}`.
- Host-prefixed canonical detail routes are canonical detail routes too.
- `useFocusDetailPresentation`: true when a canonical detail route is active and the layout is phone-like.
- `useFocusListPresentation`: true when a canonical list route is active and the layout is phone-like.

These derived values decide props and wrapper classes:

- Hide sidebars for focus detail presentations.
- Hide trailing stack sidebars for focus PR detail.
- Apply the existing mobile/focus detail sizing tokens to canonical detail routes when they are rendered in focus presentation.
- Keep PR detail tab navigation on canonical route builders while the current route is canonical.
- Keep issue selection on canonical route builders while the current route is canonical.

No presentation decision calls `navigate()` or `replaceUrl()`.

Resize handling should only update presentation state. It must not trigger pull, issue, detail, or activity store reloads beyond the loads that already happen because the active route or filters changed.

## Route Construction Boundary

All route construction must go through the existing shared helpers:

- `packages/ui/src/routes.ts` for UI route builders such as `buildPullRequestRoute`, `buildPullRequestFilesRoute`, `buildIssueRoute`, and their focus counterparts.
- `packages/ui/src/api/provider-routes.ts` for provider-aware host/default-host decisions under those builders.

Components must not hand-build canonical, focus, mobile, or host-prefixed PR/issue URLs. This keeps GitHub, GitLab, Gitea, Forgejo, and self-hosted provider routes on the same identity rules.

## Component Boundaries

The key implementation boundary is reusable focus presentation, not route family. Canonical routes at narrow widths and `/focus` routes should select from the same list/detail presentation components with different route builders and optional shell chrome.

Keep the change mostly in the frontend app shell and shared list/detail view props.

`frontend/src/App.svelte`:

- Remove the resize-driven call that rewrites canonical `activity`, `pulls`, and `issues` routes.
- Replace it with derived presentation choices.
- Continue to recognize explicit `/m/...` and `/focus/...` routes when those routes are actually active.
- Route canonical PR/issue list and detail pages at narrow widths through the same focus presentation path as explicit `/focus` routes.
- Pass focus presentation props/classes into `PRListView` and `IssueListView` for canonical routes at narrow widths and for explicit `/focus` routes.

`packages/ui/src/views/PRListView.svelte`:

- Continue accepting `selectedPR` and `detailTab`.
- Add or reuse props for hiding sidebars and trailing stack UI.
- Ensure `selectDetailTab()` uses route builders for the current route family: canonical PR builders for `/pulls/...`, focus PR builders for `/focus/pulls/...`.
- Allow a focus presentation class or mode so the same component tree can render narrow canonical routes and explicit `/focus` routes.

`packages/ui/src/views/IssueListView.svelte`:

- Continue accepting `selectedIssue`.
- Add or reuse props for hiding sidebars.
- Allow the same focus presentation class or mode for canonical issue routes and explicit `/focus` issue routes.

Shared detail components:

- Reuse the existing mobile/focus sizing tokens where possible.
- Avoid duplicating detail markup solely because one route is canonical and another is `/focus`.
- Prefer extracting shared presentation wrappers or props over maintaining parallel canonical and focus component trees.

If a selected PR or issue is missing or still loading while a canonical route is rendered in focus presentation, the view should use the same loading, stale-detail, or missing-selection behavior as the corresponding `/focus` presentation. It should not bounce the user to a list route or rewrite the URL to recover.

## Mobile and Focus Routes

The design does not need to delete `/m` or `/focus` to fix the bug.

However, those routes should be treated as explicit destinations rather than automatic responsive rewrites. This keeps the current phone-first surfaces available while removing the irreversible resize behavior.

`/focus` should remain only if it reuses the same PR and issue focus presentation that canonical routes use when narrow. Its value is route semantics and shell choice, not a separate copy of phone-detail behavior.

For example:

- Opening `/m/pulls` directly still shows the mobile PR list.
- A phone activity card may still navigate to `/focus/pulls/...` if that is the intended phone-first flow.
- A desktop user on `/pulls/...` who resizes narrow stays on `/pulls/...` while seeing the focus PR detail presentation.
- Both `/pulls/...` at narrow width and `/focus/pulls/...` use the same PR focus presentation code.
- Both `/issues/...` at narrow width and `/focus/issues/...` use the same issue focus presentation code.

This avoids preserving complexity for the resize case while keeping compatibility with existing links and tests until a later cleanup decides whether `/focus` should be folded into canonical routes entirely.

## Browser History

Viewport changes must not add or replace history entries.

Only explicit user navigation should change the location:

- Clicking nav links.
- Selecting a PR or issue from a list.
- Changing PR detail tabs.
- Opening an explicit desktop or mobile route link.
- Browser back/forward.

This means a user can:

1. Open `/pulls/github/acme/widgets/1/files`.
2. Resize narrow.
3. Inspect the focus files presentation.
4. Resize wide.
5. Return to the desktop files presentation at the same URL.

Back and forward navigation after a resize should replay explicit user navigations only. A viewport transition must not create an extra history stop and must not replace the current stop.

## Edge Cases

- Query strings and hash fragments are preserved exactly during viewport transitions.
- Browser back/forward after resizing follows the user's explicit navigation history, not any responsive presentation changes.
- Direct `/focus/...` visits remain `/focus/...` at wide widths. They may render with wider spacing or shell affordances if desired, but widening the viewport does not convert them to `/pulls/...` or `/issues/...`.
- PR tab changes use the current route family. On `/pulls/...` in narrow focus presentation, "Files changed" navigates to `/pulls/.../files`. On `/focus/pulls/...`, it navigates to `/focus/pulls/.../files`.
- Host-prefixed route tab changes preserve the host prefix and platform host.
- Missing or loading detail state keeps the active route stable and shows the shared focus/detail loading or empty state.

## Acceptance Criteria

- Resizing from desktop width to narrow width does not call `pushState`, `replaceState`, `navigate()`, or `replaceUrl()`.
- Resizing from narrow width back to desktop width restores the desktop presentation for the same path.
- Path, query string, and hash remain stable across resize-only transitions.
- Canonical PR details, PR files, and issue details render the same focus presentation as `/focus` when narrow.
- Canonical PR and issue list routes render the same focus list presentation as `/focus/mrs` and `/focus/issues` when narrow.
- PR tab navigation preserves the current route family and provider-aware host prefix.
- Explicit `/focus` routes still work when visited directly on narrow and wide viewports.
- Self-hosted routes such as `/host/git.example.com/pulls/gitea/org/repo/1` and `/host/git.example.com/issues/gitea/org/repo/10` keep their host prefix across resize and tab changes.
- Resize-only presentation changes do not trigger extra store reloads or duplicate detail fetches.
- Browser back/forward history contains only explicit user navigations.

## Testing

Update e2e coverage around the route-stability contract:

- Start wide on `/pulls/github/acme/widgets/1`.
- Assert desktop detail presentation.
- Resize to phone width.
- Assert the URL is still `/pulls/github/acme/widgets/1`.
- Assert the focus detail presentation is visible and the desktop list/sidebar chrome is hidden.
- Resize wide.
- Assert the URL is still `/pulls/github/acme/widgets/1`.
- Assert desktop detail presentation returns.

Repeat for:

- `/pulls/github/acme/widgets/1/files`.
- `/issues/github/acme/widgets/10`.
- `/host/git.example.com/pulls/gitea/org/widgets/1`.
- `/host/git.example.com/pulls/gitea/org/widgets/1/files`.
- `/host/git.example.com/issues/gitea/org/widgets/10`.

Keep explicit mobile route tests, but change tests that currently expect automatic redirects from canonical detail routes. They should instead assert stable URLs plus focus presentation.

For list routes, use the same principle:

- `/pulls` can render the focus PR list at narrow width without becoming `/m/pulls`.
- `/issues` can render the focus issue list at narrow width without becoming `/m/issues`.

If the implementation deliberately stages detail routes first, list route redirect tests should be marked for follow-up in the plan rather than left contradictory.

## Implementation Stages

1. Characterize the existing route parsing and route builders with focused unit tests, including default-host and `/host/{platform_host}` PR/issue detail paths.
2. Extract or expose a shared focus presentation mode for `PRListView`, `IssueListView`, and the focus shell without changing behavior.
3. Route canonical PR detail and files pages through the focus presentation at narrow widths while keeping canonical URLs and route builders.
4. Add PR e2e coverage for resize stability, files-tab navigation, query/hash preservation, and host-prefixed routes.
5. Repeat the canonical narrow focus presentation for issue detail routes.
6. Add issue e2e coverage for resize stability, query/hash preservation, and host-prefixed routes.
7. Apply the same route-stable focus-list presentation to canonical `/pulls` and `/issues` list routes at narrow widths.
8. Update or remove tests that assert automatic canonical-to-mobile redirects, keeping explicit `/m` and `/focus` direct-visit coverage.

## Risks

The main risk is keeping two route families and accidentally letting them drift into two implementations. The mitigation is to make presentation mode a clear input to shared view components, and to keep route builders tied to the current route family.

Another risk is regressing phone-first activity flows. Those flows should keep their explicit routes in this change. The only behavior being removed is automatic URL mutation caused by viewport size.

## Out of Scope

- Removing `/m` routes.
- Removing `/focus` routes.
- Redesigning mobile activity.
- Changing provider-aware route shapes.
- Changing API behavior or stored data.

Those may be good future cleanups, but they are not required for reversible responsive detail behavior.
