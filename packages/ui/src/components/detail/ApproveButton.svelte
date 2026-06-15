<script lang="ts">
  import CheckIcon from "@lucide/svelte/icons/check";
  import { tick } from "svelte";
  import { getClient, getStores } from "../../context.js";
  import ActionButton from "../shared/ActionButton.svelte";
  import { runApprovePR, type PRDetailActionInput } from "./keyboard-actions.js";

  const client = getClient();
  const { detail, pulls } = getStores();

  interface Props {
    owner: string;
    name: string;
    number: number;
    provider: string;
    platformHost?: string | undefined;
    repoPath: string;
    size?: "sm" | "md";
    disabled?: boolean;
    /** Head commit the rendered diff showed; fallback pin on approve. */
    expectedHeadSha?: string | undefined;
    /** Latest synced PR head from the provider; preferred pin on approve. */
    platformHeadSha?: string | undefined;
    /** capabilities.mutation_head_binding for this repo's provider. */
    requireHeadPin?: boolean;
    onheadconflict?: ((reason: "stale_state" | "head_unknown", context?: string) => void) | undefined;
    oncompleted?: (() => void) | undefined;
  }

  const {
    owner,
    name,
    number,
    provider,
    platformHost,
    repoPath,
    size = "md",
    disabled = false,
    expectedHeadSha,
    platformHeadSha,
    requireHeadPin = false,
    onheadconflict,
    oncompleted,
  }: Props = $props();

  // Captured when the approval form opens: a background detail refresh
  // must not silently rebind the pin while the form is on screen. A
  // provider with SHA guards can reject a moved head against this pin.
  let pinAtOpen = $state("");

  let expanded = $state(false);
  let body = $state("");
  let submitting = $state(false);
  let error = $state<string | null>(null);
  let commentInput = $state<HTMLTextAreaElement | undefined>();

  // Reset draft state on full provider-aware PR identity change so an
  // open form with PR A's body or head pin cannot submit to PR B once
  // the route transitions — owner/name/number alone can collide across
  // providers, hosts, and nested repo paths.
  $effect(() => {
    void provider;
    void platformHost;
    void repoPath;
    void owner;
    void name;
    void number;
    expanded = false;
    body = "";
    error = null;
    pinAtOpen = "";
  });

  $effect(() => {
    if (!expanded) return;

    void tick().then(() => {
      commentInput?.focus();
    });
  });

  function buildInput(): PRDetailActionInput {
    return {
      pr: {
        State: "open", IsDraft: false, MergeableState: "",
        platform_head_sha: pinAtOpen,
      },
      ref: { provider, platformHost, owner, name, repoPath },
      number,
      viewerCan: {
        approve: true, merge: false, markReady: false,
        approveWorkflows: false,
      },
      repoSettings: null,
      stale: disabled,
      requireHeadPin,
      stores: { detail, pulls },
      client,
      approveCommentBody: body,
      ...(pinAtOpen !== "" && { expectedHeadSha: pinAtOpen }),
      onHeadConflict: handleHeadConflict,
    };
  }

  function handleHeadConflict(reason: "stale_state" | "head_unknown", context?: string): void {
    expanded = false;
    error = null;
    pinAtOpen = "";
    onheadconflict?.(reason, context);
  }

  async function handleApprove(): Promise<void> {
    if (disabled || submitting) return;
    submitting = true;
    error = null;
    try {
      await runApprovePR(buildInput());
      body = "";
      expanded = false;
      oncompleted?.();
    } catch (err) {
      if (expanded) {
        error = err instanceof Error ? err.message : String(err);
      }
    } finally {
      submitting = false;
    }
  }
</script>

<div class={["approve-section", expanded && "approve-section--open"]}>
  <ActionButton
    class="btn btn--approve"
    onclick={() => {
      if (disabled || submitting) return;
      if (!expanded) pinAtOpen = (platformHeadSha ?? expectedHeadSha ?? "").trim();
      expanded = !expanded;
    }}
    disabled={disabled || submitting}
    ariaExpanded={expanded}
    tone="success"
    surface="soft"
    title={expanded
        ? "Close the approval form"
        : "Open the approval form to submit a code review on this pull request"}
    label="Approve"
    shortLabel="Approve"
    {size}
  >
    <CheckIcon size="14" strokeWidth="2.4" aria-hidden="true" />
  </ActionButton>

  {#if expanded}
    <div class="approve-popover" role="dialog" aria-label="Approve pull request">
      <textarea
        bind:this={commentInput}
        class="approve-comment"
        placeholder="Leave an optional comment…"
        bind:value={body}
        rows={3}
      ></textarea>
      {#if error}
        <p class="approve-error">{error}</p>
      {/if}
      <div class="approve-actions">
        <ActionButton
          class="btn btn--secondary"
          onclick={() => { expanded = false; }}
          disabled={submitting}
          tone="neutral"
          surface="outline"
        >
          Cancel
        </ActionButton>
        <ActionButton
          class="btn btn--primary btn--green"
          onclick={() => void handleApprove()}
          disabled={submitting || disabled}
          tone="success"
          surface="solid"
          title="Submit an approving code review on this pull request"
        >
          {submitting ? "Approving\u2026" : "Approve"}
        </ActionButton>
      </div>
    </div>
  {/if}
</div>

<style>
  .approve-section {
    position: relative;
    display: inline-flex;
    flex-direction: column;
    align-items: flex-start;
    max-width: 100%;
  }

  .approve-section--open {
    z-index: 30;
  }

  .approve-popover {
    position: absolute;
    top: calc(100% + 8px);
    left: 0;
    display: flex;
    flex-direction: column;
    gap: 8px;
    width: min(360px, calc(100vw - 32px));
    padding: 10px;
    background: var(--bg-surface);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    box-shadow: 0 14px 32px rgba(0, 0, 0, 0.2);
  }

  .approve-comment {
    width: 100%;
    min-height: 74px;
    font-size: var(--font-size-root);
    padding: 9px 10px;
    background: var(--bg-inset);
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    color: var(--text-primary);
    resize: vertical;
    max-height: 150px;
    line-height: 1.5;
    box-shadow: inset 0 1px 0 rgba(255, 255, 255, 0.03);
  }

  .approve-comment:focus {
    border-color: var(--accent-blue);
    outline: none;
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent-blue) 32%, transparent);
  }

  .approve-error {
    font-size: var(--font-size-sm);
    color: var(--accent-red);
  }

  .approve-actions {
    display: flex;
    gap: 8px;
    justify-content: flex-end;
    width: 100%;
  }

  @media (max-width: 420px) {
    .approve-popover {
      width: min(320px, calc(100vw - 24px));
    }
  }
</style>
