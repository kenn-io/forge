<script lang="ts">
  import { Button, Card, autoReposition, dismissable, floatingPopoverStyle } from "@kenn-io/kit-ui";
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import CheckIcon from "@lucide/svelte/icons/check";
  import { Effect } from "effect";
  import { tick, untrack } from "svelte";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { getStores } from "../../context.js";
  import { isProblem, problemConflictContext, problemConflictReason } from "../../api/problems.js";
  import type { ProviderRouteRef } from "../../api/provider-routes.js";
  import { showFlash } from "../../stores/flash.svelte.js";
  import { runApprovePR, type PRDetailActionInput } from "./keyboard-actions.js";

  const { detail } = getStores();
  const runtime = getAppRuntime();

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
  let reviewMenuOpen = $state(false);
  let requestChangesForm = $state(false);
  let menuTrigger = $state<HTMLButtonElement>();
  let menuEl = $state<HTMLUListElement>();
  let menuStyle = $state("");
  let body = $state("");
  let submitting = $state(false);
  let submittingAction = $state<"approve" | "request_changes" | null>(null);
  let sectionEl = $state<HTMLDivElement | undefined>();
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
    reviewMenuOpen = false;
    body = "";
    pinAtOpen = "";
    generationAtOpen = routeGeneration;
  });

  $effect(() => {
    if (!expanded) return;
    void requestChangesForm;
    const execution = untrack(() => runtime.runCommand(
      Effect.promise(() => tick()).pipe(
        Effect.andThen(Effect.sync(() => commentInput?.focus())),
      ),
      {
        operation: "focus pull request review comment",
        safeContext: { provider, platformHost: platformHost ?? "", owner, name, number },
        onFailure: () => {},
      },
    ));
    return execution.interrupt;
  });

  function buildInput(callbacks: {
    onHandledHeadConflict: () => void;
    onCompleted: () => void;
    onError: (message: string) => void;
    onSettled: () => void;
  }): PRDetailActionInput {
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
      stores: { detail },
      approveCommentBody: body,
      ...(pinAtOpen !== "" && { expectedHeadSha: pinAtOpen }),
      onHeadConflict: (...args) => {
        callbacks.onHandledHeadConflict();
        handleHeadConflict(...args);
      },
      onCompleted: callbacks.onCompleted,
      onError: callbacks.onError,
      onSettled: callbacks.onSettled,
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

  function handleApprove(): void {
    if (disabled || submitting) return;
    submitting = true;
    submittingAction = "approve";
    let handledHeadConflict = false;
    runApprovePR(
      buildInput({
        onHandledHeadConflict: () => {
          handledHeadConflict = true;
        },
        onCompleted: () => {
          body = "";
          expanded = false;
          oncompleted?.();
        },
        onError: (message) => {
          if (!handledHeadConflict) showFlash(message, { tone: "danger" });
        },
        onSettled: () => {
          submitting = false;
          submittingAction = null;
        },
      }),
    );
  }

  // Mirrors handleApprove/runApprovePR on purpose: the same pinAtOpen
  // captured when the form opened is submitted as expected_head_sha.
  // Request changes must not carry a stronger (or weaker) provider
  // submission contract than approve.
  function handleRequestChanges(): void {
    if (disabled || submitting || body.trim() === "") return;
    submitting = true;
    submittingAction = "request_changes";
    let handledHeadConflict = false;
    detail.requestPullChanges(
      { provider, platformHost, owner, name, repoPath },
      number,
      {
        body: body.trim(),
        ...(pinAtOpen !== "" && { expected_head_sha: pinAtOpen }),
      },
      {
        onProblem: (problem) => {
          const reason = isProblem(problem) ? problemConflictReason(problem) : undefined;
          if (reason === "stale_state" || reason === "head_unknown") {
            handledHeadConflict = true;
            handleHeadConflict(
              reason,
              isProblem(problem) ? problemConflictContext(problem) : undefined,
              pinAtOpen,
              { provider, platformHost, owner, name, repoPath },
              number,
            );
          }
        },
        onSuccess: () => {
          body = "";
          expanded = false;
          oncompleted?.();
        },
        onFailure: (message) => {
          if (!handledHeadConflict) showFlash(message, { tone: "danger" });
        },
        onSettled: () => {
          submitting = false;
          submittingAction = null;
        },
      },
    );
  }

  function positionReviewMenu(): void {
    if (!menuTrigger || !menuEl) return;
    menuStyle = floatingPopoverStyle({
      trigger: menuTrigger.getBoundingClientRect(),
      viewportWidth: window.innerWidth,
      viewportHeight: window.innerHeight,
      popoverWidth: menuEl.offsetWidth,
      popoverHeight: menuEl.offsetHeight,
      align: "start",
      triggerGap: 2,
    });
  }

  function attachMenuTrigger(node: HTMLElement): () => void {
    const button = node.querySelector("button");
    menuTrigger = button ?? undefined;
    button?.setAttribute("aria-haspopup", "menu");
    function handleKeydown(event: KeyboardEvent): void {
      if ((event.key === "ArrowDown" || event.key === "ArrowUp") && !disabled && !submitting) {
        event.preventDefault();
        reviewMenuOpen = true;
      }
    }
    node.addEventListener("keydown", handleKeydown);
    return () => {
      node.removeEventListener("keydown", handleKeydown);
      menuTrigger = undefined;
    };
  }

  function portalReviewMenu(node: HTMLElement): () => void {
    const host = sectionEl?.closest<HTMLElement>(".actions-menu-wrap--menu, .kit-modal-panel") ?? document.body;
    host.appendChild(node);
    return () => node.remove();
  }

  $effect(() => {
    if (!canRequestChanges) {
      reviewMenuOpen = false;
      return;
    }
    if (!reviewMenuOpen) return;
    const execution = untrack(() => runtime.runCommand(
      Effect.promise(() => tick()).pipe(
        Effect.andThen(Effect.sync(() => {
          positionReviewMenu();
          menuEl?.querySelector("button")?.focus();
        })),
      ),
      { operation: "open pull request review menu", safeContext: {}, onFailure: () => {} },
    ));
    const cleanups = [
      dismissable({
        owners: () => [menuTrigger, menuEl],
        dismiss: () => { reviewMenuOpen = false; },
        escapeFocus: () => menuTrigger,
      }),
      autoReposition(() => [menuEl, menuTrigger], positionReviewMenu),
    ];
    return () => {
      execution.interrupt();
      cleanups.forEach((cleanup) => cleanup());
    };
  });

  function openReviewForm(requestChanges = false): void {
    if (disabled || submitting) return;
    if (!expanded) {
      pinAtOpen = (platformHeadSha ?? expectedHeadSha ?? "").trim();
      generationAtOpen = routeGeneration;
    }
    requestChangesForm = requestChanges;
    expanded = true;
  }

  function handleDocumentPointerDown(event: PointerEvent): void {
    if (submitting) return;
    if (event.target instanceof Node && !sectionEl?.contains(event.target) && !menuEl?.contains(event.target)) {
      expanded = false;
      reviewMenuOpen = false;
    }
  }
</script>

<svelte:document onpointerdowncapture={handleDocumentPointerDown} />

<div bind:this={sectionEl} class={["approve-section", (expanded || reviewMenuOpen) && "approve-section--open"]}>
  <div class={["review-buttons", `review-buttons--${size}`, canRequestChanges && "review-buttons--split"]}>
    <Button
      class="btn btn--approve"
      onclick={() => {
        if (disabled || submitting) return;
        reviewMenuOpen = false;
        if (expanded) expanded = false;
        else openReviewForm();
      }}
      disabled={disabled || submitting}
      ariaExpanded={expanded}
      tone="success"
      surface="soft"
      title={expanded
          ? "Close the approval form"
          : title ?? "Open the approval form to submit a code review on this pull request"}
      label="Approve"
      {size}
    >
      <CheckIcon size="14" strokeWidth="2.4" aria-hidden="true" />
    </Button>
    {#if canRequestChanges}
      <span class="review-options" {@attach attachMenuTrigger}>
        <Button
          class="review-options-button"
          ariaLabel="Review options"
          title={title ?? "More review actions"}
          disabled={disabled || submitting}
          ariaExpanded={reviewMenuOpen}
          tone="success"
          surface="soft"
          {size}
          onclick={() => { reviewMenuOpen = !reviewMenuOpen; }}
        >
          <ChevronDownIcon size="12" strokeWidth="2" aria-hidden="true" />
        </Button>
      </span>
    {/if}
  </div>

  {#if reviewMenuOpen && canRequestChanges}
    <ul
      bind:this={menuEl}
      class="review-menu kit-popover-card"
      role="menu"
      aria-label="Review actions"
      style={menuStyle}
      {@attach portalReviewMenu}
    >
      <li role="none">
        <button
          type="button"
          role="menuitem"
          disabled={disabled || submitting}
          onclick={() => {
            reviewMenuOpen = false;
            openReviewForm(true);
          }}
          onkeydown={(event) => {
            if (event.key === "Tab") {
              menuTrigger?.focus();
              reviewMenuOpen = false;
            }
            else if (["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) event.preventDefault();
          }}
        >Request changes</button>
      </li>
    </ul>
  {/if}

  {#if expanded}
    <div class="approve-popover" role="dialog" aria-label="Submit pull request review">
      <Card level="default" padding="sm" class="approve-popover-card">
        <textarea
          bind:this={commentInput}
          class="approve-comment"
          aria-label={requestChangesForm ? "Requested changes" : "Review comment"}
          placeholder={requestChangesForm ? "Explain the changes you are requesting…" : "Leave an optional comment…"}
          bind:value={body}
          rows={3}
        ></textarea>
        <div class="approve-actions">
        <Button
          class="btn btn--secondary"
          {size}
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
          {size}
            onclick={handleRequestChanges}
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
        {#if !requestChangesForm}
          <Button
            class="btn btn--primary btn--green"
          {size}
            onclick={handleApprove}
            disabled={submitting || disabled}
            tone="success"
            surface="solid"
            title="Submit an approving code review on this pull request"
          >
            {submittingAction === "approve" ? "Approving\u2026" : "Approve"}
          </Button>
        {/if}
        </div>
      </Card>
    </div>
  {/if}
</div>

<style>
  .review-buttons {
    --review-options-width: var(--kit-control-height, 28px);
    display: inline-flex;
    min-width: 0;
    max-width: 100%;
    width: 100%;
  }

  .review-buttons--sm {
    --review-options-width: 24px;
  }

  .review-buttons :global(.btn--approve) {
    flex: 1 1 auto;
    min-width: 0;
  }

  .review-buttons--split :global(.btn--approve) {
    border-radius: var(--kit-control-radius, var(--radius-sm)) 0 0 var(--kit-control-radius, var(--radius-sm));
  }

  .review-options {
    display: inline-flex;
    flex: 0 0 auto;
  }

  .review-options :global(.review-options-button) {
    flex-shrink: 0;
    width: var(--review-options-width);
    padding: 0;
    border-left: 0;
    border-radius: 0 var(--kit-control-radius, var(--radius-sm)) var(--kit-control-radius, var(--radius-sm)) 0;
  }

  .review-menu {
    position: fixed;
    z-index: var(--z-popover, 1001);
    min-width: 180px;
    max-width: calc(100vw - 16px);
    margin: 0;
    padding: var(--space-2);
    list-style: none;
  }

  .review-menu button {
    width: 100%;
    min-height: 30px;
    padding: 0 var(--space-3);
    border: 0;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text-primary);
    font: inherit;
    font-size: var(--font-size-xs);
    text-align: left;
    cursor: pointer;
  }

  .review-menu button:hover:not(:disabled),
  .review-menu button:focus-visible {
    background: var(--bg-surface-hover);
  }

  .review-menu button:focus-visible {
    outline: 2px solid var(--accent-blue);
    outline-offset: -1px;
  }

  .review-menu button:disabled {
    color: var(--text-faint);
    cursor: default;
  }

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
