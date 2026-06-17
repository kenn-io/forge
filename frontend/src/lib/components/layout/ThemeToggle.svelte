<!--
  The header theme toggle. Extracted from AppHeader so it is a real, reusable
  component that can be mounted in isolation by a browser-tier test instead of
  only through the full app shell. Renders a filled moon in light mode (the
  scoped fill rule below overrides lucide's stroke-only glyph) and a sun in dark
  mode, toggling the shared theme store on click.
-->
<script lang="ts">
  import MoonIcon from "@lucide/svelte/icons/moon";
  import SunIcon from "@lucide/svelte/icons/sun";
  import { isDark, toggleTheme } from "../../stores/theme.svelte.js";
  import HeaderIconButton from "./HeaderIconButton.svelte";
</script>

<HeaderIconButton onclick={toggleTheme} title="Toggle theme">
  {#if isDark()}
    <SunIcon size="14" aria-hidden="true" />
  {:else}
    <span data-filled-icon="moon">
      <MoonIcon size="14" aria-hidden="true" />
    </span>
  {/if}
</HeaderIconButton>

<style>
  [data-filled-icon="moon"] :global(svg path) {
    fill: currentColor;
    stroke: none;
  }
</style>
