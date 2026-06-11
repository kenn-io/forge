<script lang="ts">
  import type { KataTaskAPI, KataTaskViewName } from "../../api/kata/taskTypes.js";
  import KataWorkspace from "./KataWorkspace.svelte";

  interface Props {
    api?: KataTaskAPI | undefined;
    selectedIssueUID?: string | null | undefined;
    routeViewName?: KataTaskViewName | null | undefined;
    routeScopeUID?: string | null | undefined;
    onSelectedIssueChange?: ((uid: string | null) => void) | undefined;
    onRouteStateChange?: (
      (state: { issue?: string | null; view?: KataTaskViewName | null; scope?: string | null }) => void
    ) | undefined;
    onOpenMessage?: ((messageId: number) => void) | undefined;
  }

  let {
    api = undefined,
    selectedIssueUID = null,
    routeViewName = null,
    routeScopeUID = null,
    onSelectedIssueChange = undefined,
    onRouteStateChange = undefined,
    onOpenMessage = undefined,
  }: Props = $props();
</script>

<KataWorkspace
  {api}
  {selectedIssueUID}
  {routeViewName}
  {routeScopeUID}
  {onSelectedIssueChange}
  {onRouteStateChange}
  {onOpenMessage}
/>
