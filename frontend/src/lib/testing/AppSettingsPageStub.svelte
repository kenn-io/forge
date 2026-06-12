<!--
  Test stub for SettingsPage in App-level Vitest tests. Mirrors the real
  page's `.settings-page` root class so tests can assert that App mounted
  the settings view (matching the Playwright spec's `.settings-page` wait)
  without rendering the heavyweight real page.
-->
<script lang="ts">
  // Script block present so svelte2tsx emits a typed module declaration;
  // svelte-check otherwise reports the import as implicitly `any`.
</script>

<div class="settings-page" data-testid="settings-page-stub">Settings page stub</div>
