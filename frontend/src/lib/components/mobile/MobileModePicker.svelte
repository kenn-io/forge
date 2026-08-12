<script lang="ts">
  import { SelectDropdown, type SelectDropdownOption } from "@kenn-io/kit-ui";
  import type { ModeVisibility } from "../../api/types.js";
  import type { Page } from "../../stores/router.svelte.js";

  interface Props {
    page: Page;
    isModeVisible: (mode: keyof ModeVisibility) => boolean;
    onNavigate: (path: string) => void;
  }

  let { page, isModeVisible, onNavigate }: Props = $props();

  const selectedPath = $derived.by(() => {
    if (
      page === "mobile-workspaces" ||
      page === "mobile-workspace-terminal" ||
      page === "mobile-workspace-item"
    ) {
      return "/m/workspaces";
    }
    if (page === "mobile-pulls") return "/m/pulls";
    if (page === "mobile-issues") return "/m/issues";
    return "/m";
  });

  const options = $derived<SelectDropdownOption[]>(
    [
      { mode: "activity" as const, value: "/m", label: "Activity" },
      { mode: "pulls" as const, value: "/m/pulls", label: "PRs" },
      { mode: "issues" as const, value: "/m/issues", label: "Issues" },
      { mode: "workspaces" as const, value: "/m/workspaces", label: "Workspaces" },
    ].filter((option) => isModeVisible(option.mode)),
  );
</script>

<div class="mobile-mode-picker">
  <SelectDropdown title="Phone mode" value={selectedPath} {options} onchange={onNavigate} />
</div>

<style>
  .mobile-mode-picker {
    position: relative;
    min-width: 0;
  }

  .mobile-mode-picker :global(.kit-select-dropdown) {
    width: 100%;
    min-width: 0;
  }

  .mobile-mode-picker :global(.kit-select-dropdown__trigger) {
    min-height: var(--mobile-chrome-hit-target, 2.75rem);
    padding: 0 2rem 0 0.75rem;
    border: thin solid var(--border-default);
    border-radius: var(--radius-md);
    color: var(--text-primary);
    background: var(--bg-inset);
    font: inherit;
    font-size: var(--font-size-md);
    font-weight: 650;
  }

  .mobile-mode-picker :global(.kit-select-dropdown__trigger:focus-visible) {
    outline: 2px solid var(--accent-blue);
    outline-offset: 2px;
  }

  .mobile-mode-picker :global(.kit-select-dropdown__option) {
    min-height: var(--mobile-chrome-hit-target, 2.75rem);
    font-size: var(--font-size-md);
  }
</style>
