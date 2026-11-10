<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, setContext, untrack, type ComponentProps } from "svelte";
  import type { GeneratedClient } from "../../api/generated-api.js";
  import { makeAppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import { STORES_KEY } from "../../context.js";
  import { createSettingsStore } from "../../stores/settings.svelte.js";
  import ItemActionsMenu from "./ItemActionsMenu.svelte";

  type Props = {
    client: GeneratedClient;
    settings?: ReturnType<typeof createSettingsStore>;
  } & Omit<ComponentProps<typeof ItemActionsMenu>, "open">;
  let { client, settings, ...props }: Props = $props();
  let open = $state(true);

  const runtime = makeAppRuntime(untrack(() => client));
  setAppRuntime(runtime);
  setContext(STORES_KEY, {
    settings: untrack(() => settings) ?? createSettingsStore(),
    events: { subscribeWorkspaceEvents: () => () => undefined },
  });
  onDestroy(() => {
    Effect.runFork(runtime.disposeEffect);
  });
</script>

<ItemActionsMenu bind:open {...props} />
