<script lang="ts">
  import { Button, Card } from "@kenn-io/kit-ui";
  import CheckIcon from "@lucide/svelte/icons/check";
  import { tick } from "svelte";
  import { getClient, getStores } from "../../context.js";
  import { isProblem, problemConflictContext, problemConflictReason } from "../../api/problems.js";
  import { providerItemPath, providerRouteParams, type ProviderRouteRef } from "../../api/provider-routes.js";
  import { showFlash } from "../../stores/flash.svelte.js";
  import { submitApprovePR, type PRDetailActionInput } from "./keyboard-actions.js";

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
    supportedReviewActions?: string[];
    routeGeneration?: number;
    onheadconflict?: ((
      reason: "stale_state" | "head_unknown",
      context: string | undefined,
      expectedHeadSha: string,
      ref: ProviderRouteRef,
      number: number,
      routeGeneration: number,
    ) => void) | undefined;
    oncompleted?: (() => void) | undefined;
    /** Tooltip override; pass the unavailable_reason when disabling. */
    title?: string | undefined;
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
    supportedReviewActions = [],
    routeGeneration = 0,
    onheadconflict,
    oncompleted,
    title = undefined,
  }: Props = $props();

  // Captured when the approval form opens: a background detail refresh
  // must not silently rebind the pin while the form is on screen. A
  // provider with SHA guards can reject a moved head against this pin.
  // Approve and Request changes submit this same pin — the two form
  // actions share one head-binding contract.
  let pinAtOpen = $state("");
  let generationAtOpen = $state(0);

  let expanded = $state(false);
  let body = $state("");
  let submitting = $state(false);
  let submittingAction = $state<"approve" | "request_changes" | null>(null);
  let commentInput = $state<HTMLTextAreaElement | undefined>();
  const canRequestChanges = $derived(supportedReviewActions.includes("request_changes"));

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
    pinAtOpen = "";
    generationAtOpen = routeGeneration;
  });

  $effect(() => {
    if (!expanded) return;

    void tick().then(() => {
      commentInput?.focus();
    });
  });

  function buildInput(onHandledHeadConflict?: () => void): PRDetailActionInput {
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
      onHeadConflict: (...args) => {
        onHandledHeadConflict?.();
        handleHeadConflict(...args);
      },
    };
  }

  function handleHeadConflict(
    reason: "stale_state" | "head_unknown",
    context: string | undefined,
    failedHeadSha: string,
    failedRef: ProviderRouteRef,
    failedNumber: number,
  ): void {
    const failedGeneration = generationAtOpen;
    expanded = false;
    pinAtOpen = "";
    onheadconflict?.(reason, context, failedHeadSha, failedRef, failedNumber, failedGeneration);
  }

  async function handleApprove(): Promise<void> {
    if (disabled || submitting) return;
    submitting = true;
    submittingAction = "approve";
    let handledHeadConflict = false;
    try {
      const approved = await submitApprovePR(buildInput(() => {
        handledHeadConflict = true;
      }));
      if (!approved) return;
      body = "";
      expanded = false;
      oncompleted?.();
      try {
        await Promise.all([
          detail.loadDetail(owner, name, number, { provider, platformHost, repoPath }),
          pulls.loadPulls(),
        ]);
      } catch {
        showFlash("Pull request approved, but it could not be refreshed.");
      }
    } catch (err) {
      if (!handledHeadConflict) showFlash(err instanceof Error ? err.message : String(err), { tone: "danger" });
    } finally {
      submitting = false;
      submittingAction = null;
    }
  }

  // Mirrors handleApprove/submitApprovePR on purpose: the same pinAtOpen
  // captured when the form opened is submitted as expected_head_sha.
  // Request changes must not carry a stronger (or weaker) provider
  // submission contract than approve.
  async function handleRequestChanges(): Promise<void> {
    if (disabled || submitting || body.trim() === "") return;
    submitting = true;
    submittingAction = "request_changes";
    let handledHeadConflict = false;
    try {
      const { error: requestError } = await client.POST(providerItemPath("pulls", {
        provider, platformHost, owner, name, repoPath,
      }, "/request-changes"), {
        params: { path: { ...providerRouteParams({ provider, platformHost, owner, name, repoPath }), number } },
        body: {
          body: body.trim(),
          ...(pinAtOpen !== "" && { expected_head_sha: pinAtOpen }),
        },
      });
      if (requestError) {
        const reason = isProblem(requestError) ? problemConflictReason(requestError) : undefined;
        if (reason === "stale_state" || reason === "head_unknown") {
          handledHeadConflict = true;
          handleHeadConflict(
            reason,
            isProblem(requestError) ? problemConflictContext(requestError) : undefined,
			pinAtOpen,
			{ provider, platformHost, owner, name, repoPath },
			number,
          );
        }
        throw new Error(requestError.detail ?? requestError.title ?? "failed to request changes");
      }
      body = "";
      expanded = false;
      oncompleted?.();
      try {
        await Promise.all([
          detail.loadDetail(owner, name, number, { provider, platformHost, repoPath }),
          pulls.loadPulls(),
        ]);
      } catch {
        showFlash("Changes were requested, but the pull request could not be refreshed.");
      }
    } catch (err) {
      if (!handledHeadConflict) showFlash(err instanceof Error ? err.message : String(err), { tone: "danger" });
    } finally {
      submitting = false;
      submittingAction = null;
    }
  }
</script>

<div class={["approve-section", expanded && "approve-section--open"]}>
  <Button
    class="btn btn--approve"
    onclick={() => {
      if (disabled || submitting) return;
      if (!expanded) {
        pinAtOpen = (platformHeadSha ?? expectedHeadSha ?? "").trim();
        generationAtOpen = routeGeneration;
      }
      expanded = !expanded;
    }}
    disabled={disabled || submitting}
    ariaExpanded={expanded}
    tone="success"
    surface="soft"
    title={expanded
        ? "Close the approval form"
        : title ?? "Open the approval form to submit a code review on this pull request"}
    label="Approve"
    shortLabel="Approve"
    {size}
  >
    <CheckIcon size="14" strokeWidth="2.4" aria-hidden="true" />
  </Button>

  {#if expanded}
    <div class="approve-popover" role="dialog" aria-label="Submit pull request review">
      <Card level="default" padding="sm" class="approve-popover-card">
        <textarea
          bind:this={commentInput}
          class="approve-comment"
          placeholder="Leave an optional comment…"
          bind:value={body}
          rows={3}
        ></textarea>
        <div class="approve-actions">
        <Button
          class="btn btn--secondary"
          onclick={() => { expanded = false; }}
          disabled={submitting}
          tone="neutral"
          surface="outline"
        >
          Cancel
        </Button>
        {#if canRequestChanges}
          <Button
            class="btn btn--request-changes"
            onclick={() => void handleRequestChanges()}
            disabled={submitting || disabled || body.trim() === ""}
            tone="danger"
            surface="solid"
            title={body.trim() === ""
              ? "Add a comment explaining the requested changes"
              : "Submit a review requesting changes on this pull request"}
          >
            {submittingAction === "request_changes" ? "Submitting…" : "Request changes"}
          </Button>
        {/if}
        <Button
          class="btn btn--primary btn--green"
          onclick={() => void handleApprove()}
          disabled={submitting || disabled}
          tone="success"
          surface="solid"
          title="Submit an approving code review on this pull request"
        >
          {submittingAction === "approve" ? "Approving\u2026" : "Approve"}
        </Button>
        </div>
      </Card>
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
    width: min(360px, calc(100vw - 32px));
  }

  :global(.approve-popover-card .kit-card__body) {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  .approve-comment {
    width: 100%;
    min-height: 74px;
    font-size: var(--font-size-root);
    padding: var(--space-2) var(--space-3);
    background: var(--bg-surface);
    border: var(--border-width) solid var(--border-default);
    border-radius: var(--radius-md);
    color: var(--text-primary);
    resize: vertical;
    max-height: 150px;
    line-height: 1.5;
    /* kit-ui-check-ignore: inset top-edge highlight; kit shadow tokens are outer drop shadows */
    box-shadow: inset 0 1px 0 rgba(255, 255, 255, 0.03);
  }

  .approve-comment:focus {
    border-color: var(--accent-blue);
    outline: none;
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent-blue) 32%, transparent);
  }

  .approve-actions {
    display: flex;
    gap: 8px;
    justify-content: flex-end;
    width: 100%;
  }

  @media (max-width: 640px) {
    .approve-popover {
      width: min(320px, calc(100vw - 24px));
    }
  }
</style>
