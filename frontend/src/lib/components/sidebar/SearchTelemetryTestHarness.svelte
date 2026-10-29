<script lang="ts">
  import { untrack } from "svelte";
  import type { AppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import PullList from "./PullList.svelte";
  import IssueList from "./IssueList.svelte";
  import FocusListView from "../../views/FocusListView.svelte";

  const { runtime, issues, presentation = "sidebar" }: { runtime: AppRuntime; issues: boolean; presentation?: "sidebar" | "focus" | "phone" } = $props();
  setAppRuntime(untrack(() => runtime));
</script>

{#if presentation !== "sidebar"}
  <FocusListView listType={issues ? "issues" : "mrs"} phonePresentation={presentation === "phone"} routeFamily={presentation === "phone" ? "canonical" : "focus"} />
{:else if issues}<IssueList />{:else}<PullList />{/if}
