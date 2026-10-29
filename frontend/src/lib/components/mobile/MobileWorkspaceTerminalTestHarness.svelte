<script lang="ts">
  import { untrack, type ComponentProps } from "svelte";
  import MobileWorkspaceTerminal from "./MobileWorkspaceTerminal.svelte";

  // App toggles `visible` alone. Rerendering through testing-library replaces
  // every prop at once, which reruns effects a real route change never would.
  const { initialVisible = true, ...props }: Omit<ComponentProps<typeof MobileWorkspaceTerminal>, "visible"> & { initialVisible?: boolean } = $props();
  let visible = $state(untrack(() => initialVisible));
</script>

<button type="button" onclick={() => (visible = !visible)}>Toggle terminal visibility</button>
<MobileWorkspaceTerminal {...props} {visible} />
