<script module lang="ts">
  export type MergeWarningEntry = {
    kind: "conflict" | "blocked" | "behind" | "required-checks" | "server";
    text: string;
  };
</script>

<script lang="ts">
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import ExternalLinkIcon from "@lucide/svelte/icons/external-link";
  import GitMergeIcon from "@lucide/svelte/icons/git-merge";
  import HistoryIcon from "@lucide/svelte/icons/history";
  import InfoIcon from "@lucide/svelte/icons/info";
  import ListChecksIcon from "@lucide/svelte/icons/list-checks";
  import ShieldAlertIcon from "@lucide/svelte/icons/shield-alert";
  import { Chip } from "@kenn-io/kit-ui";

  interface Props {
    warnings: MergeWarningEntry[];
    pullURL: string;
    providerLabel: string;
    expanded?: boolean;
    ontoggle?: ((expanded: boolean) => void) | undefined;
  }

  const rowIcons = {
    conflict: { icon: GitMergeIcon, tone: "row-icon-amber" },
    blocked: { icon: ShieldAlertIcon, tone: "row-icon-amber" },
    behind: { icon: HistoryIcon, tone: "row-icon-muted" },
    "required-checks": { icon: ListChecksIcon, tone: "row-icon-red" },
    server: { icon: InfoIcon, tone: "row-icon-muted" },
  } as const;

  let {
    warnings,
    pullURL,
    providerLabel,
    expanded = $bindable(false),
    ontoggle,
  }: Props = $props();

  const hasConflict = $derived(warnings.some((warning) => warning.kind === "conflict"));
  const countLabel = $derived(
    `${warnings.length} merge warning${warnings.length === 1 ? "" : "s"}`,
  );
  const chipLabel = $derived(hasConflict ? "Conflicts" : countLabel);
  const chipAriaLabel = $derived(hasConflict ? `Merge conflicts, ${countLabel}` : countLabel);

  function toggleExpanded(): void {
    const next = !expanded;
    if (ontoggle) {
      ontoggle(next);
      return;
    }
    expanded = next;
  }
</script>

{#if warnings.length > 0}
  <div class="merge-warnings-status">
    <Chip size="sm"
      interactive={true}
      tone={hasConflict ? "warning" : "neutral"}
      uppercase={false}
      ariaLabel={chipAriaLabel}
      dataTestid="merge-warnings-chip"
      onclick={toggleExpanded}
      title={expanded ? "Collapse merge warnings" : "Expand merge warnings"}
      {expanded}
    >
      <GitMergeIcon size={12} strokeWidth={2.3} aria-hidden="true" />
      <span>{chipLabel}</span>
      {#snippet trailing()}
        <ChevronDownIcon
          class={["chip-chevron", expanded && "chip-chevron--open"].filter(Boolean).join(" ")}
          size={12}
          strokeWidth={2.4}
          aria-hidden="true"
        />
      {/snippet}
    </Chip>

    {#if expanded}
      <div class="merge-warnings-collapse">
        <div class="merge-warnings-panel" role="region" aria-label="Merge warnings">
          {#each warnings as warning, index (`${index}-${warning.kind}`)}
            {@const row = rowIcons[warning.kind]}
            <div class="merge-warning-line">
              <span class={`merge-warning-icon ${row.tone}`} aria-hidden="true">
                <row.icon size={14} strokeWidth={2.2} />
              </span>
              <span class="merge-warning-text">{warning.text}</span>
            </div>
          {/each}
          <a
            class="merge-warnings-link"
            href={pullURL}
            target="_blank"
            rel="noopener noreferrer"
          >
            <span class="merge-warning-icon" aria-hidden="true">
              <ExternalLinkIcon size={14} strokeWidth={2.2} />
            </span>
            <span class="merge-warning-text">View on {providerLabel}</span>
          </a>
        </div>
      </div>
    {/if}
  </div>
{/if}

<style>
  .merge-warnings-status {
    display: contents;
  }

  :global(.chip-chevron) {
    flex-shrink: 0;
    vertical-align: middle;
    transition: transform 0.15s;
  }

  :global(.chip-chevron--open) {
    transform: rotate(180deg);
  }

  /* Full-width row in the wrapping chips-row: order pushes the panel
   * below every chip, matching StackStatus's collapse behavior. */
  .merge-warnings-collapse {
    order: 999;
    flex-basis: 100%;
    width: 100%;
    min-width: 0;
    margin-top: 4px;
  }

  /* Same panel language as .ci-checks / .stack-panel: inset card with
   * separated icon rows; tone lives in the icon, not the text. */
  .merge-warnings-panel {
    display: flex;
    flex-direction: column;
    width: 100%;
    background: var(--bg-inset);
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-md);
    overflow: auto;
    flex-shrink: 0;
    max-height: min(340px, 50vh);
  }

  .merge-warning-line,
  .merge-warnings-link {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 12px;
    font-size: var(--font-size-sm);
    color: var(--text-primary);
  }

  .merge-warning-line + .merge-warning-line,
  .merge-warning-line + .merge-warnings-link {
    border-top: 1px solid var(--border-muted);
  }

  .merge-warning-icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
    width: 16px;
  }

  .merge-warning-icon.row-icon-amber {
    color: var(--accent-amber);
  }

  .merge-warning-icon.row-icon-red {
    color: var(--accent-red);
  }

  .merge-warning-icon.row-icon-muted {
    color: var(--text-muted);
  }

  .merge-warning-text {
    min-width: 0;
    overflow-wrap: anywhere;
  }

  .merge-warnings-link {
    color: var(--text-secondary);
    text-decoration: none;
  }

  .merge-warnings-link:hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }
</style>
