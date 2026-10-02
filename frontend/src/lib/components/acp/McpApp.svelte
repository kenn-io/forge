<script lang="ts">
  import { Effect } from "effect";
  import { untrack } from "svelte";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { hostApp, type AppDescriptor } from "./mcp-app.js";

  let { app }: { app: AppDescriptor } = $props();
  const runtime = getAppRuntime();
  let status = $state("Loading widget…");
  let height = $state(320);
  let requestedLink = $state<string>();

  function attach(frame: HTMLIFrameElement) {
    const descriptor = app;
    return untrack(() => {
      const execution = runtime.runCommand(Effect.scoped(hostApp(frame, descriptor, runtime, (nextStatus, nextHeight) => {
        status = nextStatus;
        if (nextHeight !== undefined) height = nextHeight;
      }, (url) => { requestedLink ??= url; })), {
        operation: "mount MCP App", safeContext: {},
        onFailure: () => { status = "Widget could not load. Try reopening the chat."; },
      });
      return execution.interrupt;
    });
  }
</script>

<section class="mcp-app" aria-label={app.title}>
  <header><strong>{app.title}</strong><span role="status">{status}</span></header>
  {#if requestedLink}
    <div class="link-request">
      <span>Open widget link:</span>
      <a href={requestedLink} target="_blank" rel="noopener noreferrer">{requestedLink}</a>
      <button type="button" onclick={() => { requestedLink = undefined; }}>Dismiss</button>
    </div>
  {/if}
  <iframe {@attach attach} title={app.title} sandbox="allow-scripts allow-same-origin" referrerpolicy="no-referrer" style:height={`${height}px`}></iframe>
</section>

<style>
  .mcp-app { width: 100%; min-width: 0; border: 1px solid var(--border-default); border-radius: var(--radius-md); overflow: hidden; }
  header { display: flex; align-items: center; justify-content: space-between; gap: var(--space-3); padding: var(--space-2) var(--space-3); font-size: var(--font-size-sm); }
  header span { color: var(--text-muted); font-size: var(--font-size-xs); }
  .link-request { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-2); padding: var(--space-2) var(--space-3); font-size: var(--font-size-sm); }
  .link-request a { flex: 1; min-width: 0; overflow-wrap: anywhere; color: var(--accent-blue); }
  .link-request button { color: var(--text-secondary); cursor: pointer; }
  iframe { display: block; width: 100%; border: 0; background: var(--bg-primary); }
</style>
