<script lang="ts">
  import { Provider, type StoreInstances } from "@middleman/ui";
  import { client } from "../../api/runtime.js";
  import { getPage } from "../../stores/router.svelte.ts";
  import StatusBar from "./StatusBar.svelte";

  let stores = $state<StoreInstances>();
  let loaded = false;

  $effect(() => {
    if (!stores || loaded) return;
    loaded = true;
    stores.activity.initializeFromMount();
    void Promise.all([
      stores.pulls.loadPulls(),
      stores.issues.loadIssues(),
      stores.activity.loadActivity(),
    ]);
  });
</script>

<Provider {client} {getPage} bind:stores>
  <StatusBar />
</Provider>
