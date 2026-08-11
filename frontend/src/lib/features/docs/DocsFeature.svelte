<script lang="ts">
  import type { DocsRoute } from "../../api/docs/route.js";
  import type { DocsAPI } from "../../api/docs/api.js";
  import type { KataReferenceSearch } from "../../api/kata/integration.js";
  import DocsWorkspace from "../../components/docs/DocsWorkspace.svelte";

  interface Props {
    route: DocsRoute;
    onRouteChange: (next: DocsRoute, options?: { replace?: boolean }) => void;
    api?: DocsAPI;
    searchReferences?: KataReferenceSearch;
    onOpenKataReference?: (
      reference: string,
      project?: string,
      daemonId?: string,
      kind?: "reference" | "uid",
    ) => void;
  }

  let {
    route,
    onRouteChange,
    api = undefined,
    searchReferences,
    onOpenKataReference = undefined,
  }: Props = $props();
</script>

<DocsWorkspace
  {route}
  {onRouteChange}
  {api}
  {searchReferences}
  {onOpenKataReference}
/>
