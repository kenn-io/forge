<!--
  Browser-tier test harness. Composes the real HeaderIconButton, the real
  lucide Moon/Sun icons, and the real theme.svelte.ts store exactly the way
  AppHeader wires its theme toggle (onclick={toggleTheme}, Sun when dark,
  Moon otherwise). It exists only so a *.browser.svelte.ts spec can mount the
  shipped control without dragging in the app shell (Provider/stores/router).
  No styling is added here: the flex-centering under test comes from
  HeaderIconButton's own scoped CSS, and the icon swap comes from the real
  store. Mirrors the rendering behavior the theme-toggle Playwright e2e drove
  through the header, minus the AppHeader-scoped filled-moon fill rule.
-->
<script lang="ts">
  import HeaderIconButton from "./HeaderIconButton.svelte";
  // Import the two icons directly (not via icons.ts, which re-exports the whole
  // header icon set) so this harness's module graph stays minimal for a cold
  // browser test run. These are the same lucide glyphs AppHeader uses.
  import MoonIcon from "@lucide/svelte/icons/moon";
  import SunIcon from "@lucide/svelte/icons/sun";
  import { isDark, toggleTheme } from "../../stores/theme.svelte.js";
</script>

<HeaderIconButton onclick={toggleTheme} title="Toggle theme">
  {#if isDark()}
    <SunIcon size="14" aria-hidden="true" />
  {:else}
    <MoonIcon size="14" aria-hidden="true" />
  {/if}
</HeaderIconButton>
