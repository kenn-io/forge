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
  }

  let { activity: initialActivity, onUpdate }: Props = $props();
  // svelte-ignore state_referenced_locally
  let activity = $state.raw(initialActivity);
  const runtime = makeAppRuntime();
  setAppRuntime(untrack(() => runtime));

  $effect(() => {
    activity = initialActivity;
  });

  onDestroy(() => {
    Effect.runFork(runtime.disposeEffect);
  });

  function update(next: ActivitySettingsType): void {
    activity = next;
    onUpdate(next);
  }
</script>

<ActivitySettings {activity} onUpdate={update} />
