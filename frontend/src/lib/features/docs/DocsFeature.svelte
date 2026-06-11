<script lang="ts">
  import type { DocsRoute } from "../../api/docs/route.js";
  import type { DocsAPI } from "../../api/docs/api.js";
  import type { IssueSummary, KataAPI } from "../../components/docs/docsIssueTypes.js";
  import DocsWorkspace from "../../components/docs/DocsWorkspace.svelte";

  interface Props {
    route: DocsRoute;
    onRouteChange: (next: DocsRoute, options?: { replace?: boolean }) => void;
    api?: DocsAPI;
    kataAPI?: KataAPI;
    kataIssues?: readonly IssueSummary[];
    onOpenIssue?: (uid: string) => void;
    onOpenKataShortId?: (shortId: string, project?: string, daemonId?: string) => void;
  }

  let {
    route,
    onRouteChange,
    api = undefined,
    kataAPI = undefined,
    kataIssues = [],
    onOpenIssue = undefined,
    onOpenKataShortId = undefined,
  }: Props = $props();
</script>

<DocsWorkspace
  {route}
  {onRouteChange}
  {api}
  {kataAPI}
  {kataIssues}
  {onOpenIssue}
  {onOpenKataShortId}
/>
