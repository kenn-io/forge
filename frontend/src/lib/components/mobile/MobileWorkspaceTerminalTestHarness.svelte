<script lang="ts">
  import type { ComponentProps } from "svelte";
  import MobileWorkspaceTerminal from "./MobileWorkspaceTerminal.svelte";

  // App toggles `visible` alone. Rerendering through testing-library replaces
  // every prop at once, which reruns effects a real route change never would.
  const props: Omit<ComponentProps<typeof MobileWorkspaceTerminal>, "visible"> = $props();
  let visible = $state(true);
</script>

<button type="button" onclick={() => (visible = !visible)}>Toggle terminal visibility</button>
<MobileWorkspaceTerminal {...props} {visible} />
