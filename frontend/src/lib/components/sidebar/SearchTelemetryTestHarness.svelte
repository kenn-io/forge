<script lang="ts">
  import { untrack } from "svelte";
  import type { AppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import PullList from "./PullList.svelte";
  import IssueList from "./IssueList.svelte";

  const { runtime, issues }: { runtime: AppRuntime; issues: boolean } = $props();
  setAppRuntime(untrack(() => runtime));
</script>

{#if issues}<IssueList />{:else}<PullList />{/if}
