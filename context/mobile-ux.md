# Mobile UX Principles

Use this document as the intent-level guide for mobile UI work in `kenn-forge`. Read it before designing, implementing, or reviewing anything under phone routes, narrow viewports, touch-focused layouts, or mobile-specific CSS.

## Core stance

Mobile is not the desktop app squeezed into a smaller viewport. It is a separate phone-first workflow for maintainers who need to triage, inspect, and act while holding a phone.

`kenn-forge` can stay dense and information-rich, but phone density must come from hierarchy and summarization, not from tiny desktop controls, compressed split panes, or table layouts. When a phone surface feels wrong, fix the interaction model before tuning individual CSS values.

Kata and Docs remain desktop-first modes. Do not add `/m` routes or phone-only
sizing until each mode has a deliberately designed phone workflow.

## Native app shell

- Bundle the shared web UI in the native app; use the selected server only for
  APIs, event streams, and WebSockets. Do not duplicate ACP or terminal UI in
  React Native (`mobile/embedded-page.ts`).
- React Native's Gradle task inputs do not track `mobile/assets/web.bundle`;
  build release APKs through `mobile/scripts/release-android.mjs`, which forces
  the bundle task to rerun.
- Desktop layout is opt-in for tablets (`mobile/connection.ts::launchURL`).
- Frontend tests must not import from `mobile/`: its tsconfig extends
  `expo/tsconfig.base`, which frontend CI does not install. Pin shared route
  literals on each side instead.
- The native shell owns system-bar and cutout spacing. Android WebView must pass
  zeroes for those CSS safe-area insets while preserving keyboard insets
  (`mobile/patches/react-native-webview@14.0.1.patch`).
- Connected native views use the web UI's header without a native server bar.
  Connection settings live in the phone menu and desktop Settings page; Android
  Back also returns there after page history.
- Top-frame links on the server's origin and under its base path stay in the
  bundled SPA and preserve WebView history; other same-host paths open
  externally (`mobile/connection.ts::isForgePageURL`).
- Native history entries carry their URL in state: Android's HTML loader
  restores state but loses changed URLs on Back.

## Product model

On a phone the maintainer scans what changed, triages, opens the right PR or
issue, reads enough to decide whether to defer to desktop, and performs only
safe, obvious lightweight actions. Do not expose every desktop control just
because it exists.

Prefer card lists, single-column flows, focused detail routes, clearly placed
and clearly labeled primary actions, compact labeled selects for exclusive
choices (toggles for binary states), and progressive disclosure. Use horizontal
chip scrollers only for truly glanceable chips.

Avoid sidebars, split panes, desktop tables, drawer stacks, multi-row toolbars
or filter chrome, tiny icon-only actions without accessible names, and taps
that route into desktop drawer or query state with no visible phone result,
unless deliberately reimagined for touch.

## Design rules

- Build dedicated phone routes/components when the desktop interaction model
  does not fit. A `/m` route must not simply mount the desktop view inside a
  narrow wrapper.
- The phone shell has no top bar. Every phone screen's first row must end with
  the Menu button (phone modes, Forge selector, Open desktop view), or the user
  cannot switch modes (`frontend/src/lib/components/mobile/MobileNavMenu.svelte`).
- The Forge selector navigates with ordinary origin links so other tabs are not
  retargeted.
- Preserve human-facing product copy. Remove text that sounds like an
  implementation note or model instruction.
- The phone launch menu omits unavailable agents and shells; disabled choices
  waste phone space.
- Keep repository/provider identity visible enough to disambiguate similarly
  named repos, especially on activity cards and detail headers.
- Phone-like PR/issue detail, however reached, renders inside the same phone
  shell as every other phone view, with a header holding the item badge, Back,
  and the menu. Desktop-narrow focus presentation stays chrome-free.
- The phone PR header status row is one control family: one height, radius,
  size, and weight, in plain case; tone tints carry status. Pills and uppercase
  labels are desktop density devices.
- Keep the PR branch icon on the head branch's first line, with the unbroken
  arrow/base pair immediately after its final text.
- Back on a phone detail returns to the list that opened it, at the same rows.
  A fresh visit from the menu or a deep link starts at the top; a deep link with
  no origin falls back to the matching `/m` list.
- Pass the list origin through the navigate callback's `state` option, never as
  a bare second argument, and park the list position before every path that
  leaves the list, timeline events included.
- After a detail navigates again (tab switch, stack member), header Back still
  pops straight to the list while browser Back steps through the tabs
  (`frontend/src/lib/stores/mobile-list-return.ts::carryMobileListOrigin`).
- Phone Files views keep diff controls on one row and start with the file tree
  hidden; secondary controls belong in the overflow menu, not new rows.
- Diff toolbar menus must be fixed-positioned and clamped to the viewport; the
  Files view hides overflow, so trigger-anchored absolute menus get clipped.
- Mobile escape hatches to desktop views are allowed, but they must be
  intentional and not the default path.

## Responsive route and presentation model

Viewport size chooses presentation, not route identity.

- Canonical PR and issue routes (`/pulls/...`, `/pulls/.../files`,
  `/issues/...`) must not be rewritten because the viewport narrows; resizing
  down and back up stays on the same URL and returns to desktop presentation.
- Narrow canonical routes may reuse the `/focus` presentation but keep canonical
  route builders for list selection, detail tabs, and back/forward.
- `/focus` is a route over shared focus presentation components, not a separate
  implementation canonical routes cannot enter or leave responsively.
- `/m` is the phone-first route family for workflows that need a mobile shell,
  not an automatic replacement for every narrow canonical route.
- Host-prefixed routes (`/host/{platform_host}/...`) must not normalize to
  default-host or `/focus` URLs during responsive presentation changes.

Do not collapse these concepts:

- **Compact/narrow presentation**: a desktop window or pane too narrow for
  sidebars. It may use focus presentation but keeps desktop-scale typography
  and action geometry.
- **Phone-like presentation**: a touch/mobile-user-agent context where mobile
  tokens, hit targets, and phone action layouts apply.

A phone stays phone-like in landscape, up to the handheld landscape bound; wider or single-signal devices stay desktop-narrow (`frontend/src/lib/utils/phone-presentation.ts::isPhoneLikeViewport`).

Name predicates so this distinction is visible; avoid generic helpers such as `isPhoneViewport()` when the real question is "compact focus presentation?" or "phone-only sizing?".

## Typography and sizing

- Phone type uses a denser scale than kit-ui's touch scale (15px body, 13px
  metadata, 17px titles), because the larger kit scale cost too many rows on
  triage screens (`frontend/src/app.css`).
- Phone text fields stay at least `1rem`, since iOS zooms the page when a
  focused field renders below 16px; the denser scale must not reach `input`,
  `textarea`, or `select`.
- Phone detail prose renders at 14px with markdown headings on the body step,
  so long text does not dwarf the detail chrome.
- Phone controls stay compact: 40px for chrome and list controls, 38px for
  detail buttons, 32px for header status chips. Larger 44-49px targets felt
  clunky.
- Phone list rows size to their content with only the hit-target floor as a
  minimum; fixed multi-line minimum heights waste rows.
- Mobile typography, spacing, radii, and hit targets are mostly `rem`-based.
  Reuse shared phone tokens directly; components must not define private
  aliases or fallback sizes.
- Keep the global `html` root font size small for desktop/terminal stability;
  compensate inside mobile shells with mobile-scoped tokens instead.
- Do not apply mobile-scoped tokens to every narrow desktop viewport;
  desktop-narrow focus presentation stays desktop-scaled.
- Avoid raw `px` (except hairlines and tiny strokes) for mobile sizing, and
  device-DPI scaling without a proven, user-requested reason; it fights
  browser/user text scaling.

## Lists and filters

- Phone Activity (collapsed parents), PR, and issue lists start with 30 items
  and autoload one 30-item chunk per end-of-list scroll; reloads keep the
  loaded size.
- Phone startup and repository changes defer list loading to the current phone
  view, and config and provider events refresh only the current list.
- Phone Activity, PR, and issue filters use the shared repository selector;
  preset management stays outside the phone workflow.
- Those lists share one full-width search/filter trigger with repository
  selection first; each view owns its filter content.

## Routing expectations

- Phone list routes open phone-appropriate focused detail routes when the user
  is already in a phone route family.
- Do not route desktop-narrow list clicks into `/focus` unless the current
  route family is already `/focus`.
- Focused detail tabs must use the active route family's builders: focus
  builders for `/focus/...`, canonical builders for `/pulls/...` and `/issues/...`.
- Automatic responsive redirects should be rare. When needed, preserve deep-link
  intent and never bounce focused/detail routes back to a landing page.
- Desktop opt-out links are acceptable but explicit and test-covered.

## Mobile workspace workflow

- Workspaces is a first-class phone mode; `/m/workspaces` uses a dedicated card
  list and never mounts the desktop workspace layout.
- Mobile workspace screens may share data, persistence, runtime, and
  focused-item primitives with desktop, but must not depend on its pane tree,
  dock, sidebar, or resize hub.
- Keep search and workspace creation inline; view controls live in a View sheet
  backed by the same persisted settings as desktop.
- Phone workspace rows show Working, Approval, Input, and Done as visible
  badges; color-only status dots are insufficient for agent state.
- PR background sync must not reflow phone or desktop detail; use the shared
  progress treatment in `context/ui-design-system.md`.
- Phone PR detail hides the kanban/review-status selector, whose purpose is not
  self-evident on a phone; desktop-narrow keeps it.
- Phone detail title actions are bordered, equal hit-target squares; the inline
  `#123` copy button stays text-sized with only the 24px WCAG floor, or the meta
  row grows to button height.
- Creating or opening a workspace from phone-like PR or issue detail must land
  in the `/m/workspaces` shell, never the desktop `/terminal/{id}` route;
  desktop-narrow keeps desktop destinations (`frontend/src/App.svelte::phoneDetailProps`).
- Phone PR primary actions render as one fill-layout action grid whose labels
  never truncate, not the desktop fit stages and actions menu.
- Opening a workspace shows exactly one terminal, with repository, branch, and
  Fleet host identity in a full-width context row. Switching sessions never
  destroys background sessions.
- Phone terminals keep direct xterm interaction for hardware keyboards and
  terminal-native controls.
- Touching xterm must not summon the software keyboard. An optional composer
  owns software-keyboard input and sends text through xterm's sanitized paste
  path and special keys through its mode-aware key path.
- Each special-key tap claims the visible pane's resize authority before
  sending input (`frontend/src/lib/components/mobile/MobileWorkspaceTerminal.svelte::sendSpecialKey`).
- Resize Android's layout viewport with the software keyboard; visual-only
  resizing leaves unused toolbar space in Brave (`frontend/index.html`). Verify
  on native Android and iOS keyboards; desktop emulation does not exercise them.
- Keep xterm's built-in renderer on Android and Firefox; Android WebGL can
  accept output while presenting a blank surface.
- Remember the selected session per workspace. A ready local workspace always
  offers its base terminal, even while session discovery is pending; Fleet
  exposes only runtime-reported sessions.
- When a session exits, fall back to another live session or the local base
  terminal.
- Create-and-launch and quick actions show preparation and agent startup before
  opening the chosen session; the base terminal stays an explicit choice. An
  older request must not clear a newer request's launch state.
- Present an acknowledged quick-action session immediately once the loaded
  workspace and Fleet host match the route, then reconcile runtime.
- An accepted queued launch may expire and report a failure only for its own
  exact claim (`frontend/src/lib/components/terminal/workspace-runtime-workflow.ts::reconcileAcceptedLaunch`).
- A linked item opened from the workspace list returns to the list without
  mounting terminal runtime; one opened from a terminal keeps that workspace and
  session mounted.
- Fleet linked-item numbers stay passive until detail routing can retain Fleet
  host identity.
- Keep opaque session identifiers out of the linked-item header.
- The phone shell has one linked-item slot per workspace; a visible associated
  PR wins over the issue or branch it was created from, so every surface names
  the same PR.
- On issue workspaces, `mr_state` and `mr_is_draft` describe the issue, so the
  associated PR badge uses a neutral color; phone and desktop share one color
  rule (`frontend/src/lib/components/terminal/workspace-item-state.ts::workspaceItemState`).
- Background workspace reloads keep the loaded terminal on transient failure.
- Test `visible` changes through a harness that owns that state: testing-library
  `rerender` replaces every prop and reruns effects the app never reruns.
- Keep launch and Stop out of the phone terminal header; they live in a Terminal
  options tray. Stop requires explicit confirmation before the runtime mutation.
- The tray can delete the workspace with the list's confirmation and force
  flow, so a terminal never forces a trip back to the list.
- Routes retain local or Fleet identity. Only authoritative deletion events
  invalidate shared workspace state; `workspaceNotFound` replaces the phone
  route with the list without publishing a deletion.
- Unavailable Fleet hosts and connection failures stay in context with Retry or
  reconnect affordances.
- ACP sessions use their own chat composer and permission controls; never
  overlay terminal input controls on a chat session.
- Workspace layouts own the bottom device safe area; ACP composers add only
  their normal gutter.

## Verification expectations

Verify mobile-visible changes with a real phone profile, not only a resized desktop viewport.

- Use a Playwright phone profile or explicit phone-like viewport/user-agent.
- When canonical PR/issue routes change responsive presentation, add a
  desktop-narrow regression: URL stays canonical, focus presentation appears,
  phone tokens do not apply, and the desktop shell returns after widening.
- Assert the phone route renders a phone shell, not the desktop layout.
- Assert no document-level horizontal overflow, and check key element bounds;
  elements can clip even when document width is fine.
- For workspace changes, also cover session switching and exit, Fleet failures,
  item round trips, and desktop workspace regressions.
- When testing through Tailscale Serve or another proxy, confirm the proxy
  target and server process so screenshots are not from stale embedded assets.
