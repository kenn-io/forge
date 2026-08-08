<script lang="ts">
  import { untrack } from "svelte";
  import type { AppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import FilterBar from "./FilterBar.svelte";

  const { runtime }: { runtime: AppRuntime } = $props();

  setAppRuntime(untrack(() => runtime));
</script>

<FilterBar />
