<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, untrack } from "svelte";
  import type { ActivitySettings as ActivitySettingsType } from "../../api/types.js";
  import { makeAppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import ActivitySettings from "./ActivitySettings.svelte";

  interface Props {
    activity: ActivitySettingsType;
    onUpdate: (activity: ActivitySettingsType) => void;
    owner?: "hub" | "local";
  }

  let { activity: initialActivity, onUpdate, owner = "local" }: Props = $props();
  let activity = $derived(initialActivity);
  const runtime = makeAppRuntime();
  setAppRuntime(untrack(() => runtime));

  onDestroy(() => {
    Effect.runFork(runtime.disposeEffect);
  });

  function update(next: ActivitySettingsType): void {
    activity = next;
    onUpdate(next);
  }
</script>

<ActivitySettings {activity} onUpdate={update} {owner} />
