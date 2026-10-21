<script lang="ts">
  import { isWorkspaceLaunchError } from "../../app/workspace-telemetry.js";
  import { getFlashes } from "../../stores/flash.svelte.js";
  import ErrorShown from "./ErrorShown.svelte";
</script>

{#each getFlashes().filter(isWorkspaceLaunchError) as flash (flash.id)}
  <ErrorShown kind="workspace_opened" scope={`flash:${flash.id}`} />
{/each}
