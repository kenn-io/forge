<script lang="ts">
  import { untrack } from "svelte";
  import type { AppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import ReviewDrawer from "./ReviewDrawer.svelte";

  const { runtime }: { runtime: AppRuntime } = $props();

  setAppRuntime(untrack(() => runtime));
</script>

<ReviewDrawer />
