<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, untrack } from "svelte";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import type { GeneratedClient } from "../../api/generated-api.js";
  import { createRepoBrowserStore } from "../../stores/repo-browser.svelte.js";
  import type { RepoBrowserRouteRef, RepoBrowserViewMode } from "../../routes.js";
  import { makeTestAppRuntime } from "../../testing/effect-layers.js";
  import RepoBrowserFeature from "./RepoBrowserFeature.svelte";

  interface Props {
    client: GeneratedClient;
    route: {
      page: "repo-browser";
      provider: string;
      platformHost?: string | undefined;
      repoPath: string;
      owner: string;
      name: string;
      refType?: string | undefined;
      refName?: string | undefined;
      refSHA?: string | undefined;
      path?: string | undefined;
      mode?: RepoBrowserViewMode | undefined;
      anchor?: string | undefined;
    };
    onRouteChange: (route: RepoBrowserRouteRef, options?: { replace?: boolean }) => void;
  }

  const { client, route, onRouteChange }: Props = $props();
  const runtime = makeTestAppRuntime(untrack(() => client));
  const store = createRepoBrowserStore();
  setAppRuntime(untrack(() => runtime));
  onDestroy(() => {
    Effect.runFork(runtime.disposeEffect);
  });
</script>

<RepoBrowserFeature {store} {route} {onRouteChange} />
