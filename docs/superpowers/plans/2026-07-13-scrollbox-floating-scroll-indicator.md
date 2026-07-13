# ScrollBox Floating Scroll Indicator Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extract the sidebar floating scroll indicator into a shared `ScrollBox` component and apply it to the big always-visible scroll panes (diff area, pull/issue detail, main list views, activity panes).

**Architecture:** `ScrollBox` replaces `SidebarScrollArea` with the same three-layer structure (positioned root → viewport with hidden native scrollbar → content) plus an auto-hiding thumb overlay, and adds a bindable `viewport` export, an `onscroll` callback, and rest-prop spread onto the viewport so complex hosts keep their imperative scroll wiring. Hosts convert by wrapping their existing scroll element with `ScrollBox` and stripping only the `overflow`/flex-sizing rules from it, keeping the element inside `children` so host-scoped CSS keeps applying.

**Tech Stack:** Svelte 5 (runes), TypeScript, Vitest (jsdom via `vp test`), testing-library/svelte.

**Spec:** `docs/superpowers/specs/2026-07-13-scrollbox-floating-scroll-indicator-design.md`

## Global Constraints

- Never use npm. Frontend deps are installed with `bun install`; tooling runs via `../node_modules/.bin/vp` from `frontend/`.
- Run all Vitest commands from `frontend/` (the svelte plugin + `../packages/ui` include live there). A misleading "invalid JS syntax `</script>`" error means wrong cwd.
- No emojis anywhere.
- Visual output must not change apart from the scrollbar treatment (native scrollbar replaced by the floating thumb).
- Every commit follows the repo commit discipline: subject explains the user-visible outcome/why, body adds motivation, attribution block `Generated with Claude Code (claude-fable-5)` + `Co-authored-by: Claude Fable 5 <noreply@anthropic.com>`. Never `--amend`, never `--no-verify`.
- Do not run `vp fmt` bare; format only named files if needed.
- `ScrollBox` is vertical-only. Do not add horizontal thumb support.

---

### Task 1: ScrollBox component replaces SidebarScrollArea

**Files:**
- Rename: `packages/ui/src/components/shared/sidebarScrollIndicator.ts` → `packages/ui/src/components/shared/scrollIndicator.ts`
- Rename: `packages/ui/src/components/shared/sidebarScrollIndicator.test.ts` → `packages/ui/src/components/shared/scrollIndicator.test.ts`
- Create: `packages/ui/src/components/shared/ScrollBox.svelte`
- Create: `packages/ui/src/components/shared/ScrollBoxTestHost.svelte`
- Create: `packages/ui/src/components/shared/ScrollBox.test.ts`
- Delete: `packages/ui/src/components/shared/SidebarScrollArea.svelte`
- Modify: `packages/ui/src/index.ts:127`
- Modify: `packages/ui/src/components/sidebar/PullList.svelte:6,427,505`
- Modify: `packages/ui/src/components/sidebar/IssueList.svelte:5,212,263`
- Modify: `frontend/src/lib/components/terminal/WorkspaceListSidebar.svelte:15,1050,1243`

**Interfaces:**
- Consumes: `getScrollIndicatorGeometry(viewportHeight, contentHeight, scrollTop)` from the renamed `scrollIndicator.ts` (logic unchanged).
- Produces: `ScrollBox` component with props `{ class?: ClassValue; dataTest?: string; label: string; onscroll?: (event: Event) => void; viewport?: HTMLDivElement (bindable); children: Snippet; ...rest spread onto viewport }`. DOM classes: `scroll-box` (root), `scroll-box__viewport` (scrolling element, `role="region"`, `aria-label={label}`, `tabindex="0"`), `scroll-box__content`, `scroll-box__indicator`/`scroll-box__thumb`. Later tasks bind `viewport` and query `.scroll-box__viewport` in tests. Exported from `packages/ui` index as `ScrollBox`.

- [ ] **Step 1: Rename the geometry helper and its test**

```bash
cd /Users/mariusvniekerk/.t3/worktrees/middleman/t3code-6b1d1387
git mv packages/ui/src/components/shared/sidebarScrollIndicator.ts packages/ui/src/components/shared/scrollIndicator.ts
git mv packages/ui/src/components/shared/sidebarScrollIndicator.test.ts packages/ui/src/components/shared/scrollIndicator.test.ts
```

In `scrollIndicator.test.ts` change the import:

```ts
import { getScrollIndicatorGeometry } from "./scrollIndicator.js";
```

(`SidebarScrollArea.svelte` still imports the old path; it is deleted in Step 4, so do not fix its import.)

- [ ] **Step 2: Write the failing ScrollBox tests**

Create `packages/ui/src/components/shared/ScrollBoxTestHost.svelte`:

```svelte
<script lang="ts">
  import ScrollBox from "./ScrollBox.svelte";

  interface Props {
    onscroll?: (event: Event) => void;
    onviewport?: (el: HTMLDivElement | undefined) => void;
  }

  const { onscroll, onviewport }: Props = $props();

  let viewport = $state<HTMLDivElement>();

  $effect(() => {
    onviewport?.(viewport);
  });
</script>

<ScrollBox label="Test scroll region" {onscroll} bind:viewport>
  <div style="height: 800px">tall content</div>
</ScrollBox>
```

Create `packages/ui/src/components/shared/ScrollBox.test.ts`. jsdom has no layout, and the test setup stubs `ResizeObserver` as a no-op, so viewport/content heights are faked at the prototype level before mount (same pattern as `DiffView.test.ts`):

```ts
import { cleanup, fireEvent, render } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import ScrollBoxTestHost from "./ScrollBoxTestHost.svelte";

let restoreHeights: (() => void) | undefined;

function stubHeights(contentHeight: number): void {
  const clientDesc = Object.getOwnPropertyDescriptor(Element.prototype, "clientHeight");
  const offsetDesc = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
  Object.defineProperty(Element.prototype, "clientHeight", {
    configurable: true,
    get(this: Element) {
      return this.classList.contains("scroll-box__viewport") ? 200 : 0;
    },
  });
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
    configurable: true,
    get(this: HTMLElement) {
      return this.classList.contains("scroll-box__content") ? contentHeight : 0;
    },
  });
  restoreHeights = () => {
    if (clientDesc) Object.defineProperty(Element.prototype, "clientHeight", clientDesc);
    if (offsetDesc) Object.defineProperty(HTMLElement.prototype, "offsetHeight", offsetDesc);
  };
}

afterEach(() => {
  cleanup();
  restoreHeights?.();
  restoreHeights = undefined;
  vi.useRealTimers();
});

function getViewport(): HTMLDivElement {
  return document.querySelector(".scroll-box__viewport") as HTMLDivElement;
}

describe("ScrollBox", () => {
  it("shows the thumb while scrolling and hides it after the timeout", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    stubHeights(800);
    render(ScrollBoxTestHost);

    const viewport = getViewport();
    expect(document.querySelector(".scroll-box__indicator.visible")).toBeNull();

    viewport.scrollTop = 120;
    await fireEvent.scroll(viewport);
    expect(document.querySelector(".scroll-box__indicator.visible")).not.toBeNull();

    await vi.advanceTimersByTimeAsync(700);
    expect(document.querySelector(".scroll-box__indicator.visible")).toBeNull();
  });

  it("keeps the thumb hidden when content fits the viewport", async () => {
    stubHeights(150);
    render(ScrollBoxTestHost);

    const viewport = getViewport();
    await fireEvent.scroll(viewport);
    expect(document.querySelector(".scroll-box__indicator.visible")).toBeNull();
  });

  it("forwards scroll events to the onscroll prop", async () => {
    stubHeights(800);
    const onscroll = vi.fn();
    render(ScrollBoxTestHost, { props: { onscroll } });

    await fireEvent.scroll(getViewport());
    expect(onscroll).toHaveBeenCalledTimes(1);
    expect(onscroll.mock.calls[0][0]).toBeInstanceOf(Event);
  });

  it("exposes the scrolling element through the bindable viewport", () => {
    stubHeights(800);
    const seen: Array<HTMLDivElement | undefined> = [];
    render(ScrollBoxTestHost, { props: { onviewport: (el) => seen.push(el) } });

    const bound = seen.at(-1);
    expect(bound).toBeInstanceOf(HTMLDivElement);
    expect(bound?.classList.contains("scroll-box__viewport")).toBe(true);
  });

  it("labels the scroll region for keyboard users", () => {
    stubHeights(800);
    render(ScrollBoxTestHost);

    const viewport = getViewport();
    expect(viewport.getAttribute("role")).toBe("region");
    expect(viewport.getAttribute("aria-label")).toBe("Test scroll region");
    expect(viewport.getAttribute("tabindex")).toBe("0");
  });
});
```

- [ ] **Step 3: Run the new tests to verify they fail**

```bash
cd /Users/mariusvniekerk/.t3/worktrees/middleman/t3code-6b1d1387/frontend
../node_modules/.bin/vp test ScrollBox
```

Expected: FAIL — `ScrollBox.svelte` does not exist (import error in the test host).

- [ ] **Step 4: Create ScrollBox.svelte and delete SidebarScrollArea.svelte**

Create `packages/ui/src/components/shared/ScrollBox.svelte`:

```svelte
<script lang="ts">
  import { onDestroy, onMount, type Snippet } from "svelte";
  import type { ClassValue, HTMLAttributes } from "svelte/elements";
  import { getScrollIndicatorGeometry } from "./scrollIndicator.js";

  interface Props extends Omit<HTMLAttributes<HTMLDivElement>, "class" | "onscroll"> {
    class?: ClassValue;
    dataTest?: string;
    label: string;
    onscroll?: (event: Event) => void;
    viewport?: HTMLDivElement;
    children: Snippet;
  }

  let {
    class: className = "",
    dataTest,
    label,
    onscroll,
    viewport = $bindable(),
    children,
    ...rest
  }: Props = $props();

  let viewportHeight = $state(0);
  let contentHeight = $state(0);
  let scrollTop = $state(0);
  let visible = $state(false);
  let hideTimer: number | undefined;
  let content: HTMLDivElement;

  const geometry = $derived(
    getScrollIndicatorGeometry(
      viewportHeight,
      contentHeight,
      scrollTop,
    ),
  );

  function handleScroll(event: Event): void {
    scrollTop = (event.currentTarget as HTMLDivElement).scrollTop;
    if (geometry.scrollable) {
      visible = true;
      window.clearTimeout(hideTimer);
      hideTimer = window.setTimeout(() => {
        visible = false;
      }, 700);
    }
    onscroll?.(event);
  }

  function updateDimensions(): void {
    if (!viewport) return;
    viewportHeight = viewport.clientHeight;
    contentHeight = content.offsetHeight;
  }

  onMount(() => {
    updateDimensions();

    const observer = new ResizeObserver(updateDimensions);
    if (viewport) observer.observe(viewport);
    observer.observe(content);

    return () => observer.disconnect();
  });

  onDestroy(() => window.clearTimeout(hideTimer));
</script>

<div class={["scroll-box", className]} data-test={dataTest}>
  <!-- Scrollable regions need keyboard access. -->
  <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
  <div
    {...rest}
    class="scroll-box__viewport"
    aria-label={label}
    bind:this={viewport}
    onscroll={handleScroll}
    role="region"
    tabindex="0"
  >
    <div
      class="scroll-box__content"
      bind:this={content}
    >
      {@render children()}
    </div>
  </div>
  <div
    class={["scroll-box__indicator", { visible: visible && geometry.scrollable }]}
    aria-hidden="true"
  >
    <span
      class="scroll-box__thumb"
      style:height={`${geometry.height}px`}
      style:transform={`translateY(${geometry.top}px)`}
    ></span>
  </div>
</div>

<style>
  .scroll-box {
    position: relative;
    flex: 1;
    min-height: 0;
    overflow: hidden;
  }

  .scroll-box__viewport {
    height: 100%;
    overflow-y: auto;
    scrollbar-width: none;
  }

  .scroll-box__viewport::-webkit-scrollbar {
    display: none;
    width: 0;
    height: 0;
  }

  .scroll-box__content {
    min-height: 100%;
  }

  .scroll-box__indicator {
    position: absolute;
    inset: 0 2px 0 auto;
    z-index: 2;
    width: 4px;
    pointer-events: none;
    opacity: 0;
    transition: opacity 160ms cubic-bezier(0.22, 1, 0.36, 1);
  }

  .scroll-box__indicator.visible {
    opacity: 1;
  }

  .scroll-box__thumb {
    display: block;
    width: 100%;
    border-radius: 999px;
    background: color-mix(in srgb, var(--text-muted) 72%, transparent);
    box-shadow: 0 0 0 1px color-mix(in srgb, var(--bg-primary) 35%, transparent);
    will-change: transform;
  }

  @media (prefers-reduced-motion: reduce) {
    .scroll-box__indicator {
      transition: none;
    }
  }

  @media (forced-colors: active) {
    .scroll-box__thumb {
      background: CanvasText;
      box-shadow: none;
    }
  }
</style>
```

Differences from `SidebarScrollArea` (everything else is a verbatim carry-over): `viewport` is a `$bindable()` prop instead of a local `let`, `handleScroll` always forwards to the `onscroll` prop (indicator state still only changes when scrollable), rest props spread onto the viewport (before the fixed attributes so ScrollBox's contract wins), and class names are `scroll-box*`.

Then delete the old component:

```bash
git rm packages/ui/src/components/shared/SidebarScrollArea.svelte
```

- [ ] **Step 5: Update the package export**

In `packages/ui/src/index.ts` replace line 127:

```ts
export { default as ScrollBox } from "./components/shared/ScrollBox.svelte";
```

(The old line exported `SidebarScrollArea` from `SidebarScrollArea.svelte`. Keep the list alphabetized if neighbors are.)

- [ ] **Step 6: Migrate the three sidebar consumers**

`packages/ui/src/components/sidebar/PullList.svelte` line 6:

```ts
import ScrollBox from "../shared/ScrollBox.svelte";
```

Line 427 `<SidebarScrollArea` → `<ScrollBox` (attributes unchanged), line 505 `</SidebarScrollArea>` → `</ScrollBox>`.

`packages/ui/src/components/sidebar/IssueList.svelte` line 5: same import change; line 212 `<SidebarScrollArea class="list-body" label="Issues">` → `<ScrollBox class="list-body" label="Issues">`; line 263 closing tag.

`frontend/src/lib/components/terminal/WorkspaceListSidebar.svelte` line 15: in the `@middleman/ui`-style braced import list, replace `SidebarScrollArea,` with `ScrollBox,`; line 1050 `<SidebarScrollArea class="sidebar-list" label="Workspaces">` → `<ScrollBox class="sidebar-list" label="Workspaces">`; line 1243 closing tag.

Verify nothing else references the old names:

```bash
cd /Users/mariusvniekerk/.t3/worktrees/middleman/t3code-6b1d1387
grep -rn "SidebarScrollArea\|sidebarScrollIndicator\|sidebar-scroll" packages/ui/src frontend/src frontend/tests
```

Expected: no matches (a `context/ui-design-system.md` match is handled in Task 6).

- [ ] **Step 7: Run tests to verify they pass**

```bash
cd /Users/mariusvniekerk/.t3/worktrees/middleman/t3code-6b1d1387/frontend
../node_modules/.bin/vp test ScrollBox scrollIndicator PullList IssueList WorkspaceListSidebar
```

Expected: PASS (all suites).

- [ ] **Step 8: Commit**

Follow the repo commit skill. Suggested subject: `refactor: generalize sidebar scroll indicator into ScrollBox`. Body: why — the floating indicator is expanding beyond sidebars (diff and detail panes come next), so the sidebar-specific component and naming are being replaced by one canonical ScrollBox with a bindable viewport for hosts with imperative scroll logic.

---

### Task 2: Diff area uses ScrollBox

**Files:**
- Modify: `packages/ui/src/components/diff/DiffView.svelte` (markup ~602-611 and 646, CSS ~693-696, script imports)
- Modify: `packages/ui/src/components/diff/DiffView.test.ts` (all `.diff-area` selectors)

**Interfaces:**
- Consumes: `ScrollBox` from Task 1 (`bind:viewport`, `onscroll`, `class`, `label`, rest-prop `style`).
- Produces: the diff scroll element in the DOM is now `.diff-area .scroll-box__viewport`; `diffArea` in the component still points at the scrolling element, so scroll restore, paging, wheel containment, active-file tracking, and the virtualizer are unchanged.

- [ ] **Step 1: Update failing tests first**

In `packages/ui/src/components/diff/DiffView.test.ts`, replace every scroll-element selector so the tests describe the new DOM:

- every `".diff-area"` string inside `querySelector(...)` calls becomes `".scroll-box__viewport"`
- every `this.classList.contains("diff-area")` inside prototype getter mocks becomes `this.classList.contains("scroll-box__viewport")`

```bash
cd /Users/mariusvniekerk/.t3/worktrees/middleman/t3code-6b1d1387/frontend
../node_modules/.bin/vp test DiffView
```

Expected: FAIL — the component still renders the plain `.diff-area` div, so `.scroll-box__viewport` queries return null.

- [ ] **Step 2: Convert the component**

In `packages/ui/src/components/diff/DiffView.svelte` add the import:

```ts
import ScrollBox from "../shared/ScrollBox.svelte";
```

Replace the scroll container markup (currently lines 602-611):

```svelte
        <ScrollBox
          class={["diff-area", { "diff-area--word-wrap": wordWrap }]}
          label="Changed file diffs"
          bind:viewport={diffArea}
          onscroll={onDiffScroll}
          style="tab-size: {tabWidth}; overscroll-behavior: contain"
        >
          <div class="diff-content" bind:this={diffContent}>
```

and change the matching closing `</div>` (line 646, the one that closed `.diff-area`) to `</ScrollBox>`. The `role="region"` / `aria-label` move into ScrollBox via `label`; `style:tab-size` and `style:overscroll-behavior` become the `style` string prop because `style:` directives do not work on components — the string spreads onto the viewport, where descendants still inherit `tab-size`.

In the CSS, delete the now-dead sizing rule:

```css
  .diff-area {
    flex: 1;
    overflow: auto;
  }
```

(`.diff-area--word-wrap` stays in use: `DiffFile.svelte:646` targets it via `:global(.diff-area--word-wrap) .file-content`, and the ScrollBox root carrying the class is still an ancestor of `.file-content`.)

Note: `diffArea` keeps its existing declaration; `bind:viewport={diffArea}` assigns the viewport element to it. The `$effect` at DiffView.svelte:564 that sets `tabIndex = -1` and attaches wheel/touch/pointer/keydown listeners operates on the bound element unchanged.

- [ ] **Step 3: Run tests to verify they pass**

```bash
cd /Users/mariusvniekerk/.t3/worktrees/middleman/t3code-6b1d1387/frontend
../node_modules/.bin/vp test DiffView DiffFile DiffFilesLayout DiffSidebar PierreFileDiff
```

Expected: PASS.

- [ ] **Step 4: Commit**

Suggested subject: `feat: floating scroll indicator on the diff views`. Body: the diff area (PR Files tab, workspace diff panel, commit diff panel) now matches the sidebars' overlay scroll treatment; the bindable viewport keeps scroll restore, paging, and the virtualizer on the real scroll element.

---

### Task 3: Pull and issue detail scrollers use ScrollBox

**Files:**
- Modify: `packages/ui/src/components/detail/PullDetail.svelte` (markup ~1526-1530 and matching close, CSS ~2465-2475, imports)
- Modify: `packages/ui/src/components/detail/IssueDetail.svelte` (markup ~899, CSS ~1339-1349, imports)

**Interfaces:**
- Consumes: `ScrollBox` (`bind:viewport`, `onscroll`, `label`).
- Produces: `pullDetailScroller` now points at the ScrollBox viewport; `.pull-detail` and `.issue-detail` remain in the DOM as non-scrolling content wrappers so their scoped CSS (padding, markdown `pre` wrapping) and browser-test selectors (`App.detail-code-wrap.browser.svelte.ts`, `App.provider-capabilities.browser.svelte.ts`) keep working.

- [ ] **Step 1: Convert PullDetail**

Add the import:

```ts
import ScrollBox from "../shared/ScrollBox.svelte";
```

Replace the scroller markup (currently lines 1526-1530):

```svelte
        <ScrollBox
          label="Pull request conversation"
          bind:viewport={pullDetailScroller}
          onscroll={handlePullDetailScroll}
        >
          <div class="pull-detail">
```

and add the extra closing `</ScrollBox>` where the old `.pull-detail` div closed. If `pullDetailScroller` is declared as a plain `let`, change it to `let pullDetailScroller = $state<HTMLDivElement>();` so the component binding is reactive-safe (keep the existing name and type).

In the `.pull-detail` CSS rule (line ~2465), remove only the scrolling/sizing lines — `flex: 1`, `min-height: 0`, `overflow-y: auto` — and keep `padding`, `display: flex`, `flex-direction: column`, `min-width: 0`, `overflow-x: hidden` (it clips wide content), and `width: 100%`. The ScrollBox root supplies `flex: 1; min-height: 0`.

- [ ] **Step 2: Convert IssueDetail**

Same import. Replace `<div class="issue-detail">` (line 899):

```svelte
    <ScrollBox label="Issue conversation">
      <div class="issue-detail">
```

with the matching `</ScrollBox>` added at the wrapper's close. In `.issue-detail` CSS (line ~1339) remove `flex: 1`, `min-height: 0`, `overflow-y: auto`; keep `padding`, `display: flex`, `flex-direction: column`, `min-width: 0`, `overflow-x: hidden`, `width: 100%`.

- [ ] **Step 3: Run tests to verify they pass**

```bash
cd /Users/mariusvniekerk/.t3/worktrees/middleman/t3code-6b1d1387/frontend
../node_modules/.bin/vp test PullDetail IssueDetail PRListView
```

Expected: PASS. The scroll save/restore logic (`rememberPullDetailScroll` / `restorePullDetailScroll`) reads and writes `pullDetailScroller.scrollTop`, which is now the viewport — no logic change.

- [ ] **Step 4: Commit**

Suggested subject: `feat: floating scroll indicator on pull and issue detail`. Body: the conversation/timeline panes now share the overlay scroll treatment; the pull detail scroll position save/restore binds to the ScrollBox viewport.

---

### Task 4: Main list views use ScrollBox

**Files:**
- Modify: `packages/ui/src/views/FocusListView.svelte` (`.list-body` div and CSS at ~463)
- Modify: `packages/ui/src/views/ReviewsView.svelte` (`.reviews-table` div and CSS at ~333)
- Modify: `packages/ui/src/views/MobileActivityView.svelte` (`.mobile-activity-scroll` div at 360, CSS at ~566, `.mobile-activity-inbox` CSS)

**Interfaces:**
- Consumes: `ScrollBox` (simple wrap; none of these views have scroll wiring).
- Produces: no new interfaces; DOM gains `.scroll-box` wrappers around existing content.

- [ ] **Step 1: Convert FocusListView**

Import `ScrollBox from "../components/shared/ScrollBox.svelte"`. First check for other `.list-body` references in this file:

```bash
grep -n "list-body" packages/ui/src/views/FocusListView.svelte
```

If the class is only the scroll div + its CSS rule, replace `<div class="list-body">` with `<ScrollBox label="Pull requests">` (and the closing tag), and delete the `.list-body { flex: 1; overflow-y: auto; }` rule — ScrollBox's root supplies the sizing. If other selectors reference it, keep the class via `class="list-body"` on the ScrollBox instead and delete only the two CSS declarations.

- [ ] **Step 2: Convert ReviewsView**

Import ScrollBox (path `../components/shared/ScrollBox.svelte`). Wrap the existing element:

```svelte
<ScrollBox label="Review queue">
  <div class="reviews-table">
    ...existing children unchanged...
  </div>
</ScrollBox>
```

In `.reviews-table` CSS remove `flex: 1` and `overflow-y: auto`; keep `display: flex; flex-direction: column;`.

- [ ] **Step 3: Convert MobileActivityView**

Import ScrollBox (path `../components/shared/ScrollBox.svelte`). Wrap:

```svelte
<section class="mobile-activity-inbox" aria-label="Mobile activity inbox">
  <ScrollBox label="Activity inbox">
    <div class="mobile-activity-scroll">
```

In CSS: add `display: flex; flex-direction: column;` to `.mobile-activity-inbox` (the ScrollBox root sizes itself with `flex: 1; min-height: 0`, which needs a flex parent; the section's only child is the scroller). In `.mobile-activity-scroll` remove `height: 100%` and `overflow-y: auto`; keep the padding block and `font-size`.

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /Users/mariusvniekerk/.t3/worktrees/middleman/t3code-6b1d1387/frontend
../node_modules/.bin/vp test FocusListView ReviewsView MobileActivityView
```

Expected: PASS.

- [ ] **Step 5: Commit**

Suggested subject: `feat: floating scroll indicator on focus, reviews, and mobile activity views`.

---

### Task 5: Activity panes use ScrollBox

**Files:**
- Modify: `packages/ui/src/components/ActivityFeed.svelte` (`.table-container` div, CSS at ~1028)
- Modify: `packages/ui/src/components/ActivityThreaded.svelte` (`.threaded-view` div, CSS at ~782)

**Interfaces:**
- Consumes: `ScrollBox` (simple wrap).
- Produces: `.threaded-view` remains the grid container, now nested inside the ScrollBox content; its sticky repo headers stick against the ScrollBox viewport (nearest scrollport).

- [ ] **Step 1: Convert ActivityFeed**

Import `ScrollBox from "./shared/ScrollBox.svelte"`. Wrap the existing element:

```svelte
<ScrollBox label="Activity feed">
  <div class="table-container">
    ...existing children unchanged...
  </div>
</ScrollBox>
```

In `.table-container` CSS remove `flex: 1` and `overflow-y: auto`; keep `padding: 0 16px`. The compact override `.activity-feed--compact .table-container { padding: 0; }` still matches (both elements stay in this component's template, and the descendant relationship holds across the ScrollBox boundary).

- [ ] **Step 2: Convert ActivityThreaded**

Import `ScrollBox from "./shared/ScrollBox.svelte"`. Wrap `<div class="threaded-view">` the same way with `label="Threaded activity"`. In `.threaded-view` CSS remove `flex: 1` and `overflow-y: auto`; keep everything else (the grid definition, padding, and the `--threaded-col-*` custom properties). Do not restructure the grid: `.repo-section` uses subgrid spanning `1 / -1` and must keep `.threaded-view` as its grid parent. Sticky repo headers now stick against the ScrollBox viewport instead of `.threaded-view` itself, which is the same visual behavior since `.threaded-view` fills the viewport width.

- [ ] **Step 3: Run tests to verify they pass**

```bash
cd /Users/mariusvniekerk/.t3/worktrees/middleman/t3code-6b1d1387/frontend
../node_modules/.bin/vp test ActivityFeed ActivityThreaded
```

Expected: PASS.

- [ ] **Step 4: Commit**

Suggested subject: `feat: floating scroll indicator on activity panes`. Body: note the sticky-header scrollport change and that `frontend/tests/e2e-full/activity-threaded-sticky.spec.ts` verifies it (run in Task 6).

---

### Task 6: Context doc update and full validation

**Files:**
- Modify: `context/ui-design-system.md:188`

**Interfaces:**
- Consumes: everything above.
- Produces: final validated branch.

- [ ] **Step 1: Update the context doc invariant**

In `context/ui-design-system.md` line 188, rewrite the `SidebarScrollArea` sentence to name `ScrollBox` and its wider scope. Keep it a terse invariant. Replacement for the two sentences that mention the component (leave the rest of the paragraph intact):

> Wrap large always-visible scroll panes (list rails, diff area, pull/issue detail, activity views) in `ScrollBox` so the floating scroll indicator overlays content, appears only during scrolling, and does not reserve a permanent gutter; bind `viewport` when a host needs imperative scroll logic. Give each scroll area a concise accessible label so keyboard users can identify and scroll the region.

Update the file-path parenthetical to point at `packages/ui/src/components/shared/ScrollBox.svelte`.

- [ ] **Step 2: Full frontend test run**

```bash
cd /Users/mariusvniekerk/.t3/worktrees/middleman/t3code-6b1d1387/frontend
../node_modules/.bin/vp test
```

Expected: PASS including the browser-lane projects. If ~38 browser files are missing or port 63315 is busy, a sibling worktree is running the browser lane — wait for the port; never kill the other process.

- [ ] **Step 3: Type/lint checks**

```bash
cd /Users/mariusvniekerk/.t3/worktrees/middleman/t3code-6b1d1387
make frontend-check-no-deps
```

Expected: PASS. (If svelte-check dies with exit 137 or phantom "Cannot find module", see the known workarounds: run via node with a heap bump; `bun install --frozen-lockfile` if node_modules is stale.)

- [ ] **Step 4: Affected Playwright e2e**

The change touches scroll geometry that `activity-threaded-sticky.spec.ts` and `diff-view.spec.ts` assert against a real browser. Run the full-stack suite:

```bash
cd /Users/mariusvniekerk/.t3/worktrees/middleman/t3code-6b1d1387
make test-e2e
```

Expected: PASS. Investigate any failure in specs touching activity, diff, detail, or sidebar scrolling before proceeding; unrelated local-baseline flakes are judged against a clean HEAD baseline, not "fixed" by widening this change.

- [ ] **Step 5: Commit**

Suggested subject: `docs: point the scroll-indicator invariant at ScrollBox`.

---

## Verification checklist (post-plan, pre-PR)

- Scroll each converted pane in the real app (`make dev` + `make frontend-dev`, or the middleman-ephemeral-dev skill): thumb appears while scrolling, fades ~0.7s after stopping, no native scrollbar, no layout shift.
- Diff view: file jump (file tree click), PageUp/PageDown paging, scroll-position restore when switching PRs, word-wrap toggle.
- Pull detail: scroll down, switch away, return — position restored.
- Threaded activity: repo headers still stick while scrolling.
- The PR adds visible UI change: capture a short video/screenshot with the capture-playwright skill before opening the PR.
