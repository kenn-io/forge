<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy } from "svelte";
  import { makeAppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import MobileNavMenu from "./MobileNavMenu.svelte";
  import MobileNavScope from "./MobileNavScope.svelte";
  import type { MobileNavMenuContext } from "./mobile-nav-menu.js";

  const { nav }: { nav?: MobileNavMenuContext } = $props();
  const runtime = makeAppRuntime();
  setAppRuntime(runtime);
  onDestroy(() => Effect.runFork(runtime.disposeEffect));
</script>

{#if nav}
  <MobileNavScope {nav}>
    <MobileNavMenu />
  </MobileNavScope>
{:else}
  <MobileNavMenu />
{/if}
