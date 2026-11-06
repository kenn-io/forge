<script lang="ts">
  import { setContext, untrack } from "svelte";
  import { createAppStores } from "../../app-stores.svelte.js";
  import type { AppRuntime } from "../../app/runtime.js";
  import type { StoreInstances } from "../../types.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import { STORES_KEY } from "../../context.js";
  import { getPage } from "../../stores/router.svelte.ts";
  import StatusBar from "./StatusBar.svelte";

  let { runtime, configureStores }: { runtime: AppRuntime; configureStores?: (stores: StoreInstances) => void } = $props();
  setAppRuntime(untrack(() => runtime));

  const stores = createAppStores({ runtime: untrack(() => runtime), getPage }).stores;
  untrack(() => configureStores?.(stores));
  setContext(STORES_KEY, stores);

  $effect(() =>
    untrack(() => {
      stores.activity.initializeFromMount();
      const polling = runtime.runCommand(stores.sync.pollingEffect, {
        operation: "poll sync status in status bar test host",
        safeContext: {},
        onFailure: () => {},
      });
      stores.pulls.loadPulls();
      stores.issues.loadIssues();
      stores.activity.loadActivity();
      return polling.interrupt;
    }),
  );
</script>

<StatusBar />
