<script lang="ts">
  import { SelectDropdown, Toggle, getHighContrast, setHighContrast } from "@kenn-io/kit-ui";
  import { getAppearanceMode, isEReader, setAppearanceMode, type AppearanceMode } from "../../stores/theme.svelte.js";
</script>

<div class="appearance-settings">
  <SelectDropdown
    title="Color theme"
    value={getAppearanceMode()}
    options={[
      { value: "system", label: "System" },
      { value: "light", label: "Light" },
      { value: "dark", label: "Dark" },
      { value: "e-reader", label: "E-reader" },
    ]}
    onchange={(value) => setAppearanceMode(value as AppearanceMode)}
  />
  {#if isEReader()}
    <p>White pages, black text, and strong borders for e-ink displays.</p>
  {:else}
    <Toggle label="High contrast" checked={getHighContrast()} onchange={setHighContrast} />
  {/if}
</div>

<style>
  .appearance-settings {
    display: grid;
    gap: 0.75rem;
  }
  p {
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
  }
</style>
