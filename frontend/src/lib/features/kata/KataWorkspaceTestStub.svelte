<script lang="ts">
  import type { KataTaskAPI, KataTaskViewName } from "../../api/kata/taskTypes.js";

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

<div
  data-testid="kata-workspace-stub"
  data-has-api={api ? "true" : "false"}
  data-selected-issue={selectedIssueUID ?? ""}
  data-route-view={routeViewName ?? ""}
  data-route-scope={routeScopeUID ?? ""}
>
  <button type="button" onclick={() => onSelectedIssueChange?.("issue-next")}>select</button>
  <button type="button" onclick={() => onRouteStateChange?.({ view: "today", scope: "project-a", issue: "issue-next" })}>route</button>
  <button type="button" onclick={() => onOpenMessage?.(42)}>message</button>
</div>
