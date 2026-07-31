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
requests.

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
teardown cancels its queued task. Diff identity changes and view teardown reset
the scheduler, cancel all queued tasks, abort active task signals, and ignore
stale completion callbacks. The file-preview cache continues to deduplicate
requests; the task abort signal is checked before every component state write,
render, or error update rather than aborting the cache's shared request.

Without a scheduler, `PierreFileDiff` retains its existing standalone behavior:
visible files may load syntax context and offscreen files do not preload it.
Demand-driven context expansion remains immediate and does not wait for the
background queue.

## Testing

- Pure scheduler tests prove the four-file ceiling, foreground priority over
  queued background work, deferred background dispatch, priority promotion,
  slot reuse, generation-safe reset, and cancellation.
- A `PierreFileDiff` component test proves an offscreen sparse diff registers
  proactive work and loads only after the scheduler starts it.
- Component tests prove teardown cancels registration, aborted completion does
  not write stale state, and manual context expansion bypasses the queue.
- A `DiffView` test proves a file-preview generation change resets the scheduler.
- Existing diff component tests continue to cover viewport rendering and manual
  context expansion.
- The full frontend unit suite and the Chromium diff-view Playwright suite cover
  integration regressions.
