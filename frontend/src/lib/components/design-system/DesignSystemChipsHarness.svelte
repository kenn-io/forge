<!--
  Browser-tier test harness. Renders the same Chip primitives, sizes, color
  classes, and casing/interactivity variants that DesignSystemPage.svelte puts
  on the /design-system route, scoped under the same [data-size] row wrappers
  the Playwright e2e selected by. It exists only so a *.browser.svelte.ts spec
  can mount the shipped Chip directly (no app shell, no stores) and read its
  real computed styles. The geometry/typography under test comes entirely from
  Chip.svelte's own scoped CSS plus the app.css design tokens; this harness
  adds only the matrix row scaffolding the page uses, never chip styling.
-->
<script lang="ts">
  // Import Chip from its source file rather than the @middleman/ui barrel.
  // The barrel re-exports the whole UI package (tiptap editor, pierre diffs,
  // dozens of lucide icons), which the browser project would have to optimize
  // mid-run and reload over. A direct source import keeps this spec's module
  // graph small so a cold `vp test run --project browser` stays deterministic.
  import Chip from "../../../../../packages/ui/src/components/shared/Chip.svelte";
</script>

<div class="harness">
  <div class="matrix-row" data-size="sm">
    <Chip size="sm" class="chip--green">Green</Chip>
    <Chip size="sm" class="chip--muted">Muted</Chip>
  </div>

  <div class="matrix-row" data-size="md">
    <Chip size="md" class="chip--green">Green</Chip>
    <Chip size="md" class="chip--muted">Muted</Chip>
  </div>

  <div class="behavior-row">
    <Chip class="chip--green">Uppercase</Chip>
    <Chip class="chip--green" uppercase={false}>plain case</Chip>
    <Chip class="chip--purple" interactive onclick={() => {}}>Interactive</Chip>
    <Chip size="sm" class="chip--muted" uppercase={false} dataTestid="descender-chip">team/inbox-view</Chip>
  </div>
</div>

<style>
  .harness {
    display: grid;
    gap: 12px;
    padding: 16px;
  }

  .matrix-row,
  .behavior-row {
    display: flex;
    flex-wrap: wrap;
    gap: 10px;
    align-items: start;
  }
</style>
