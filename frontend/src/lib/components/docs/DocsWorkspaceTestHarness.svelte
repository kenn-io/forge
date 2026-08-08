<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, untrack } from "svelte";
  import type { DocsAPI } from "../../api/docs/api.js";
  import type { DocsRoute } from "../../api/docs/route.js";
  import type { KataTaskReferenceSearch } from "../../api/kata/snapshot.js";
  import { makeAppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import DocsWorkspace from "./DocsWorkspace.svelte";

  interface Props {
    route: DocsRoute;
    onRouteChange: (route: DocsRoute, options?: { replace?: boolean }) => void;
    api: DocsAPI;
  }

  const { route, onRouteChange, api }: Props = $props();
  const runtime = makeAppRuntime();
  const searchReferences: KataTaskReferenceSearch = async () => ({
    server_instance_id: "docs-workspace-test",
    daemon_id: "docs-workspace-test",
    generation: 1,
    invalidation_epoch: 1,
    fetched_at: "2026-01-01T00:00:00Z",
    references: [],
  });
  setAppRuntime(untrack(() => runtime));
  onDestroy(() => {
    Effect.runFork(runtime.disposeEffect);
  });
</script>

<DocsWorkspace {route} {onRouteChange} {api} {searchReferences} />
