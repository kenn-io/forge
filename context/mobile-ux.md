# Mobile UX Principles

Use this document as the intent-level guide for mobile UI work in `kenn-forge`. Read it before designing, implementing, or reviewing anything under phone routes, narrow viewports, touch-focused layouts, or mobile-specific CSS.

## Core stance

Mobile is not the desktop app squeezed into a smaller viewport. It is a separate phone-first workflow for maintainers who need to triage, inspect, and act while holding a phone.

`kenn-forge` can stay dense and information-rich, but phone density must come from hierarchy and summarization, not from tiny desktop controls, compressed split panes, or table layouts.

Kata and Docs remain desktop-first modes. Do not add `/m` routes or phone-only
sizing until each mode has a deliberately designed phone workflow.

## Product model

Think about mobile work in this order:

1. **What is the maintainer trying to do on a phone?**
   - Scan what changed.
   - Triage what needs attention.
   - Open the right PR or issue quickly.
   - Read enough context to decide whether to defer to desktop.
   - Perform lightweight actions only when they are safe and obvious.

2. **What can be hidden, grouped, or deferred?**
   - Prefer summary cards, grouped events, focused detail routes, and progressive disclosure.
   - Do not expose every desktop control just because it exists.
   - Avoid sidebars, split panes, dense tables, drawer stacks, and multi-row toolbars on phone routes unless they are deliberately reimagined for touch.

3. **What should the thumb hit first?**
   - Primary actions need clear labels and comfortable hit targets.
   - Secondary filters should be compact but still readable.
   - Binary states can be toggles; mutually-exclusive choices usually belong in compact labeled dropdowns/selects rather than repeated chip rows.

## Design rules

- Build dedicated phone routes/components when the desktop interaction model does not fit. A `/m` route must not simply mount the desktop view inside a narrow wrapper.
- Preserve human-facing product copy. Remove text that sounds like an implementation note or model instruction.
- Keep repository/provider identity visible enough to disambiguate similarly named repos, especially on activity cards and detail headers.
- Give focused PR/issue detail pages their own phone shell treatment even when they reuse desktop detail components internally.
- Mobile escape hatches to desktop views are allowed, but they must be intentional and not the default path.

## Responsive route and presentation model

Viewport size chooses presentation, not route identity.

- Canonical PR and issue routes such as `/pulls/...`, `/pulls/.../files`, and `/issues/...` must not be rewritten just because the viewport becomes narrow. A user who resizes a desktop detail page down and back up should stay on the same URL and naturally return to the desktop presentation when there is room again.
- Narrow canonical PR/issue routes may reuse the same focus presentation used by `/focus`, but they must keep canonical route builders for list selection, detail-tab changes, and back/forward behavior.
- `/focus` remains a valid explicit route family, but it should be a route over shared focus presentation components, not a separate implementation that canonical routes cannot enter or leave responsively.
- `/m` remains a phone-first route family. Use it for workflows that need a mobile shell, not as an automatic replacement for every narrow canonical route.
- Keep host-prefixed identity stable too. `/host/{platform_host}/pulls/...` and `/host/{platform_host}/issues/...` should not normalize back to default-host or `/focus` URLs during responsive presentation changes.

Do not collapse these concepts:

- **Compact/narrow presentation**: a desktop window, split pane, or embedded surface that is too narrow for sidebars or dense desktop chrome. It can use focus presentation, but it should retain desktop-scale typography and desktop action geometry.
- **Phone-like presentation**: a touch/mobile-user-agent context where larger mobile tokens, hit targets, and phone-specific action layouts are appropriate.

In code and tests, name predicates so this distinction is visible. Avoid generic helpers such as `isPhoneViewport()` when the real question is either "should this route use the compact focus presentation?" or "should this surface use phone-only sizing?"

## Typography and sizing

- `frontend/src/app.css` owns the shared design tokens, including `--font-size-mobile-*`.
- Mobile typography, spacing, radii, and hit targets should be mostly `rem`-based and expressed through scoped tokens.
- The app intentionally keeps the global root font size small for desktop/terminal stability. Do not change the global `html` root just to make mobile readable.
- Compensate inside mobile shells with mobile-scoped tokens such as `--mobile-type-*`, pointing back to the app-level mobile font-size tokens where possible.
- Do not apply mobile-scoped tokens to every narrow desktop viewport. A desktop-narrow focus presentation should remain compact and desktop-scaled unless the environment is phone-like.
- Avoid raw `px` as the sizing model for mobile typography, spacing, or touch targets. Hairline borders and tiny decorative strokes are the main exceptions.
- Avoid device-DPI-specific scaling unless there is a proven, user-requested reason; it fights browser/user text scaling and makes the UI less predictable.

## Interaction patterns that usually fit phones

Prefer:

- Card lists over tables.
- Single-column flows over split panes.
- Focused detail routes over desktop drawers.
- Sticky or clearly placed primary actions over toolbar clusters.
- Compact labeled dropdowns/selects for mutually-exclusive filters.
- Horizontal chip scrollers only when the chips are truly glanceable and do not dominate vertical space.
- Progressive disclosure for metadata, timelines, and secondary actions.

Avoid by default:

- Desktop tables in phone wrappers.
- Nested sidebars or trailing panes.
- Multi-row chip/filter chrome that pushes content below the fold.
- Tiny icon-only actions without accessible names and visible context.
- Routing mobile taps into desktop drawer/query state with no visible phone result.

## Routing expectations

- Phone list/start routes should route to phone-appropriate focused detail routes when the user is already in a phone route family.
- Canonical list/detail routes should stay canonical when they are only changing presentation for a narrow viewport. Do not route desktop-narrow list clicks into `/focus` unless the current route family is already `/focus`.
- Focused detail tabs, such as PR files, must use route builders for the active route family: focus builders for `/focus/...`, canonical builders for `/pulls/...` and `/issues/...`.
- Automatic responsive redirects should be rare. Prefer presentation selection over URL replacement for canonical routes; when redirects are truly needed, preserve user intent for deep links and do not bounce focused/detail routes back to a landing page.
- Desktop opt-out links are acceptable, but they should be explicit and test-covered.

## Verification expectations

For mobile-visible changes, verify behavior with a real phone profile, not only a resized desktop viewport.

Minimum expectations for meaningful mobile UI changes:

- Use a Playwright phone profile or explicit phone-like viewport/user-agent setup appropriate for the repo's browser matrix.
- Add a separate desktop-narrow regression when canonical PR/issue routes change responsive presentation. This should prove the URL stays canonical, the focus presentation appears when narrow, the phone-only class/tokens do not apply in desktop-narrow contexts, and the desktop shell returns after widening.
- Assert the phone route renders a phone shell/component and not the desktop layout.
- Assert no document-level horizontal overflow.
- Check key element bounds for cards, filters, tabs, branch names, and action buttons; element clipping can happen even when document width is fine.
- Assert source sizing remains token/rem-based for the changed mobile surface.
- Cover click/tap flows that move from mobile lists into focused detail routes.
- When testing through Tailscale Serve or another proxy, confirm the proxy target and server process so screenshots are not from stale embedded assets.

## Review checklist

Before shipping mobile UX work, ask:

- Is this a phone-first workflow, or did we just resize desktop?
- Is the primary task obvious without scanning desktop chrome?
- Are type, spacing, and hit targets driven by mobile tokens?
- Are mobile tokens limited to phone-like contexts rather than every narrow desktop viewport?
- Did we keep the global root font size stable?
- Are provider/repo identity and item numbers still clear?
- Do taps navigate to visible phone outcomes rather than desktop-only state?
- Did focused detail and tab routes use builders for their active route family?
- Did Playwright cover both a phone profile and any desktop-narrow canonical route behavior?

If the answer to any of these is no, fix the interaction model before tuning individual CSS values.
