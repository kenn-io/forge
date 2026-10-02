<script lang="ts">
  import { Effect } from "effect";
  import { untrack } from "svelte";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { hostApp, type AppDescriptor } from "./mcp-app.js";

  let { app }: { app: AppDescriptor } = $props();
  const runtime = getAppRuntime();
  let status = $state("Loading widget…");
  let height = $state(320);

  function attach(frame: HTMLIFrameElement) {
    const descriptor = app;
    return untrack(() => {
      const execution = runtime.runCommand(Effect.scoped(hostApp(frame, descriptor, runtime, (nextStatus, nextHeight) => {
        status = nextStatus;
        if (nextHeight !== undefined) height = nextHeight;
      })), {
        operation: "mount MCP App", safeContext: {},
        onFailure: () => { status = "Widget could not load. Try reopening the chat."; },
      });
      return execution.interrupt;
    });
  }
</script>

<section class="mcp-app" aria-label={app.title}>
  <header><strong>{app.title}</strong><span role="status">{status}</span></header>
  <iframe {@attach attach} title={app.title} sandbox="allow-scripts allow-same-origin" referrerpolicy="no-referrer" style:height={`${height}px`}></iframe>
</section>

<style>
  .mcp-app { width: 100%; min-width: 0; border: 1px solid var(--border-default); border-radius: var(--radius-md); overflow: hidden; }
  header { display: flex; align-items: center; justify-content: space-between; gap: var(--space-3); padding: var(--space-2) var(--space-3); font-size: var(--font-size-sm); }
  header span { color: var(--text-muted); font-size: var(--font-size-xs); }
  iframe { display: block; width: 100%; border: 0; background: var(--bg-primary); }
</style>
