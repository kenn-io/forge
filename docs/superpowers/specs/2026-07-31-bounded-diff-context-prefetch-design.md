# Bounded diff context prefetch

## Goal

Let large diffs progressively prepare syntax-correct full-file context without
waiting for every file to approach the viewport, while ensuring that background
work cannot recreate the eager-load stall.

## Design

Each `DiffView` owns one context-prefetch scheduler with a concurrency limit of
four files. `PierreFileDiff` registers only when its sparse patch can distort
syntax highlighting and complete old/new contents are available. A registered
task owns both side requests, so the limit is four files rather than four HTTP
requests. At most eight side requests may therefore be unresolved at once.

Files inside the existing 600px intersection margin are foreground work. Their
tasks are promoted and dispatched immediately. Other registered files remain in
document order and begin from deferred background turns: `requestIdleCallback`
where available and a delayed timer fallback elsewhere. When a slot opens,
queued foreground work always wins over background work. Already-started work
is not preempted merely because another file becomes visible.

The scheduler is scoped to the rendered diff identity: provider, host,
repository path, item number, and the diff store's file-preview generation. The
store increments that generation whenever workspace identity, base, commit/range
scope, whitespace mode, or snapshot changes clear the preview cache. Component
teardown cancels its queued task. Both the view and each registering component
align the scheduler to the current identity, making generation changes safe
regardless of Svelte parent/child effect order. Identity changes and view
teardown cancel all queued tasks, abort active task signals, and ignore stale
completion callbacks. Because the file-preview cache's shared requests are not
aborted, stale active tasks retain their scheduler slots until they settle; the
four-file ceiling therefore applies across generations. Cache generations drop
references to old results, and stale component state is never retained.

A failed background attempt is speculative: it does not show an error or latch
the file as permanently failed. The file is registered again when it enters the
foreground. Failure of that foreground attempt keeps the existing sparse render
and displays the context error without an automatic retry loop.

Collapsed files do not mount `PierreFileDiff` and therefore do not register
prefetch work. Background dispatch uses a 500ms idle timeout, so work still
progresses when the tab or browser does not grant an ordinary idle period.

Without a scheduler, `PierreFileDiff` retains its existing standalone behavior:
visible files may load syntax context and offscreen files do not preload it.
Demand-driven context expansion remains immediate and does not wait for the
background queue.

## Testing

- Pure scheduler tests prove the four-file ceiling, foreground priority over
  queued background work, deferred background dispatch, priority promotion,
  slot reuse, generation-safe reset, cross-generation slot accounting, and
  cancellation.
- A `PierreFileDiff` component test proves an offscreen sparse diff registers
  proactive work and loads only after the scheduler starts it.
- Component tests prove teardown cancels registration, aborted completion does
  not write stale state, a failed speculative attempt retries in the foreground,
  and manual context expansion bypasses the queue.
- A `DiffView` test proves a file-preview generation change aligns the scheduler.
- A Playwright test uses the real seeded git repository, HTTP API, and preview
  cache to hold one generation's requests across a whitespace refresh. It proves
  the process never exceeds four file tasks/eight side requests and that a
  distant file is prepared before scrolling reaches it.
- Existing diff component tests continue to cover viewport rendering and manual
  context expansion.
- The full frontend unit suite and the Chromium diff-view Playwright suite cover
  integration regressions.

The measurable acceptance boundary is four unresolved file tasks (eight side
requests), including stale generations, with no visible loading placeholder
when the tested distant file is reached after its proactive requests complete.
The existing large-diff fast-scroll Playwright case remains the interaction
latency guard; this scheduler does not introduce a hardware-specific frame-time
budget.
