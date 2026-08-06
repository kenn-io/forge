<script lang="ts">
  import type { DocsRoute } from "../../api/docs/route.js";
  import type { DocsAPI } from "../../api/docs/api.js";
  import type { KataTaskReferenceSearch } from "../../api/kata/snapshot.js";
  import type { IssueSummary } from "../../components/docs/docsIssueTypes.js";
  import DocsWorkspace from "../../components/docs/DocsWorkspace.svelte";

  interface Props {
    route: DocsRoute;
    onRouteChange: (next: DocsRoute, options?: { replace?: boolean }) => void;
    api?: DocsAPI;
    searchReferences: KataTaskReferenceSearch;
    kataIssues?: readonly IssueSummary[];
    onOpenIssue?: (uid: string, daemonId?: string) => void;
    onOpenKataShortId?: (shortId: string, project?: string, daemonId?: string) => void;
  }

  let {
    route,
    onRouteChange,
    api = undefined,
    searchReferences,
    kataIssues = [],
    onOpenIssue = undefined,
    onOpenKataShortId = undefined,
  }: Props = $props();
</script>

<DocsWorkspace
  {route}
  {onRouteChange}
  {api}
  {searchReferences}
  {kataIssues}
  {onOpenIssue}
  {onOpenKataShortId}
/>
