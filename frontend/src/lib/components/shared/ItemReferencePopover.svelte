<script lang="ts">
  import { Effect } from "effect";
  import { tick, untrack } from "svelte";
  import {
    autoReposition,
    Chip,
    ColorLabel,
    DiffStats,
    floatingPopoverStyle,
    formatRelativeTime,
    Spinner,
  } from "@kenn-io/kit-ui";
  import {
    canonicalProvider,
    providerUsesHostRoute,
    resolvedPlatformHost,
  } from "../../api/provider-routes.js";
  import { RepositoryReads } from "../../api/repository-reads.js";
  import { repositoryKeyFromWire } from "../../api/repository-key.js";
  import type { Issue, PullRequest, RepoCatalog } from "../../api/types.js";
  import type { AppExecution } from "../../app/runtime.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import {
    ItemPreviewCache,
    loadItemPreviewEffect,
    PREVIEW_LOAD_ERROR_MESSAGE,
    type ItemPreview,
    type ItemPreviewOutcome,
  } from "../../stores/item-preview.js";
  import { isPullMerging } from "../../stores/pull-merges.svelte.js";
  import { getRoute, getURLChangeCount, type RoutableItemRef } from "../../stores/router.svelte.js";
  import { bucketCIChecks, parseCIChecks, safeDiagnosticText } from "../../utils/ci-buckets.js";
  import {
    buildItemReferenceHref,
    type ProviderContext,
    type ResolvableItemReference,
  } from "../../utils/item-reference.js";
  import {
    findItemRefAnchor,
    isTrackedItemReference,
    resolveAnchorItemReference,
  } from "../../utils/itemRefHandler.js";
  import { summarizeMarkdownBody } from "../../utils/markdown-excerpt.js";
  import { localDateTimeLabel } from "../../utils/time.js";
  import { hasMergeQueuePosition, mergeQueueIndicatorLabel } from "../detail/merge-queue.js";
  import ItemStateChip from "./ItemStateChip.svelte";
  import PullStatusTokens from "./PullStatusTokens.svelte";
  import RepoPrivateIcon from "./RepoPrivateIcon.svelte";
  import WorkspaceIndicator from "./WorkspaceIndicator.svelte";

  const OPEN_DELAY_MS = 450;
  const CLOSE_DELAY_MS = 200;
  const LOADING_DELAY_MS = 100;
  const EDGE_GAP = 8;
  const TRIGGER_GAP = 8;
  const MAX_VISIBLE_LABELS = 4;

  interface Props {
    getProviderContexts?: () => ReadonlyArray<ProviderContext>;
    getCurrentItem?: () => RoutableItemRef | null;
    onOpenItem?: (item: RoutableItemRef) => void;
  }

  let {
    getProviderContexts = () => [],
    getCurrentItem = () => null,
    onOpenItem = undefined,
  }: Props = $props();

  type CardState =
    | { readonly kind: "loading"; readonly ref: ResolvableItemReference }
    | { readonly kind: "loaded"; readonly ref: ResolvableItemReference; readonly preview: ItemPreview }
    | { readonly kind: "not-synced"; readonly ref: ResolvableItemReference }
    | { readonly kind: "not-found"; readonly ref: ResolvableItemReference }
    | { readonly kind: "error"; readonly ref: ResolvableItemReference; readonly message: string };

  const runtime = getAppRuntime();
  const popoverId = $props.id();
  const cache = new ItemPreviewCache();

  let visible = $state(false);
  let card = $state.raw<CardState | null>(null);
  let popoverEl = $state<HTMLDivElement>();
  let popoverStyle = $state("");

  let activeAnchor: HTMLAnchorElement | null = null;
  let describedAnchor: HTMLAnchorElement | null = null;
  let pendingAnchor: HTMLAnchorElement | null = null;
  let lastPointer: { x: number; y: number } | null = null;
  let openTimer: ReturnType<typeof setTimeout> | undefined;
  let closeTimer: ReturnType<typeof setTimeout> | undefined;
  let loadingTimer: ReturnType<typeof setTimeout> | undefined;
  let previewExecution: AppExecution<ItemPreviewOutcome, never> | null = null;
  let requestToken = 0;

  let getCachedRepositories: () => readonly RepoCatalog[] | undefined = () => undefined;

  function clearOpenTimer(): void {
    if (openTimer === undefined) return;
    clearTimeout(openTimer);
    openTimer = undefined;
  }

  function clearCloseTimer(): void {
    if (closeTimer === undefined) return;
    clearTimeout(closeTimer);
    closeTimer = undefined;
  }

  function clearLoadingTimer(): void {
    if (loadingTimer === undefined) return;
    clearTimeout(loadingTimer);
    loadingTimer = undefined;
  }

  function clearAllTimers(): void {
    clearOpenTimer();
    clearCloseTimer();
    clearLoadingTimer();
  }

  function detachDescribedBy(): void {
    if (!describedAnchor) return;
    const existing = describedAnchor.getAttribute("aria-describedby")?.trim() ?? "";
    const remaining = existing
      ? existing.split(/\s+/).filter((token) => token !== popoverId)
      : [];
    if (remaining.length > 0) {
      describedAnchor.setAttribute("aria-describedby", remaining.join(" "));
    } else {
      describedAnchor.removeAttribute("aria-describedby");
    }
    describedAnchor = null;
  }

  function attachDescribedBy(anchor: HTMLAnchorElement): void {
    if (describedAnchor && describedAnchor !== anchor) {
      detachDescribedBy();
    }
    const existing = anchor.getAttribute("aria-describedby")?.trim() ?? "";
    const tokens = existing ? existing.split(/\s+/) : [];
    if (!tokens.includes(popoverId)) {
      anchor.setAttribute("aria-describedby", [...tokens, popoverId].join(" "));
    }
    describedAnchor = anchor;
  }

  function hide(): void {
    requestToken += 1;
    clearAllTimers();
    previewExecution?.interrupt();
    previewExecution = null;
    detachDescribedBy();
    pendingAnchor = null;
    activeAnchor = null;
    lastPointer = null;
    visible = false;
    card = null;
  }

  function scheduleClose(): void {
    clearOpenTimer();
    pendingAnchor = null;
    if (!visible && !activeAnchor) return;
    clearCloseTimer();
    closeTimer = setTimeout(() => {
      closeTimer = undefined;
      hide();
    }, CLOSE_DELAY_MS);
  }

  function triggerRectForAnchor(anchor: HTMLAnchorElement, pointer: { x: number; y: number } | null): DOMRect {
    const rects = Array.from(anchor.getClientRects()).filter((rect) => rect.width > 0 && rect.height > 0);
    if (rects.length <= 1) return rects[0] ?? anchor.getBoundingClientRect();
    if (pointer) {
      const containing = rects.find(
        (rect) =>
          pointer.x >= rect.left && pointer.x <= rect.right && pointer.y >= rect.top && pointer.y <= rect.bottom,
      );
      if (containing) return containing;
    }
    return rects[0]!;
  }

  async function position(): Promise<void> {
    await tick();
    if (!visible || !activeAnchor || !popoverEl) return;
    if (!activeAnchor.isConnected) {
      hide();
      return;
    }
    const trigger = triggerRectForAnchor(activeAnchor, lastPointer);
    const outOfView =
      trigger.bottom < 0 ||
      trigger.top > window.innerHeight ||
      trigger.right < 0 ||
      trigger.left > window.innerWidth;
    if (outOfView) {
      hide();
      return;
    }
    const width = popoverEl.offsetWidth || 360;
    const height = popoverEl.offsetHeight || 120;
    popoverStyle = floatingPopoverStyle({
      trigger,
      viewportWidth: window.innerWidth,
      viewportHeight: window.innerHeight,
      popoverWidth: width,
      popoverHeight: height,
      align: "start",
      edgeGap: EDGE_GAP,
      triggerGap: TRIGGER_GAP,
      placement: "auto",
    });
  }

  function applyOutcome(
    token: number,
    urlCountAtOpen: number,
    ref: ResolvableItemReference,
    outcome: ItemPreviewOutcome,
  ): void {
    if (token !== requestToken || getURLChangeCount() !== urlCountAtOpen || !activeAnchor?.isConnected) return;
    clearLoadingTimer();
    if (outcome.status === "untracked") {
      hide();
      return;
    }
    if (outcome.status === "found") {
      card = { kind: "loaded", ref, preview: outcome.preview };
    } else if (outcome.status === "not-synced") {
      card = { kind: "not-synced", ref };
    } else if (outcome.status === "not-found") {
      card = { kind: "not-found", ref };
    } else {
      card = { kind: "error", ref, message: outcome.message };
    }
    visible = true;
    attachDescribedBy(activeAnchor);
    void position();
  }

  function openForAnchor(anchor: HTMLAnchorElement, ref: ResolvableItemReference): void {
    clearAllTimers();
    previewExecution?.interrupt();
    previewExecution = null;
    pendingAnchor = null;

    if (!anchor.isConnected) return;

    const token = ++requestToken;
    const urlCountAtOpen = getURLChangeCount();
    activeAnchor = anchor;

    const cached = cache.get(ref);
    if (cached !== undefined) {
      applyOutcome(token, urlCountAtOpen, ref, cached);
      return;
    }

    // Avoid a brief loading flash when the local server responds in a few milliseconds.
    loadingTimer = setTimeout(() => {
      loadingTimer = undefined;
      if (token !== requestToken || getURLChangeCount() !== urlCountAtOpen || !activeAnchor?.isConnected) return;
      card = { kind: "loading", ref };
      visible = true;
      attachDescribedBy(activeAnchor);
      void position();
    }, LOADING_DELAY_MS);

    previewExecution = untrack(() =>
      runtime.runCommand(
        loadItemPreviewEffect(ref, cache).pipe(
          Effect.tap((outcome) => Effect.sync(() => applyOutcome(token, urlCountAtOpen, ref, outcome))),
        ),
        {
          operation: "load item reference preview",
          safeContext: {
            provider: ref.provider,
            owner: ref.owner,
            name: ref.name,
            number: ref.number,
          },
          onFailure: () => {
            applyOutcome(token, urlCountAtOpen, ref, { status: "error", message: PREVIEW_LOAD_ERROR_MESSAGE });
          },
        },
      ),
    );
  }

  function isCurrentOpenItem(ref: ResolvableItemReference): boolean {
    const current = getCurrentItem();
    if (!current || current.number !== ref.number) return false;
    const refProvider = canonicalProvider(ref.provider);
    if (refProvider !== canonicalProvider(current.provider)) return false;
    const refHost = resolvedPlatformHost(ref.provider, ref.platformHost).toLowerCase();
    const currentHost = resolvedPlatformHost(current.provider, current.platformHost).toLowerCase();
    if (refHost !== currentHost) return false;
    const currentPath = (current.repoPath || `${current.owner}/${current.name}`).toLowerCase();
    if (ref.repoPath.toLowerCase() !== currentPath) return false;
    if (
      ref.itemType !== undefined &&
      ref.itemType !== current.itemType &&
      (refProvider === "gitlab" || refProvider === "bitbucket")
    ) {
      return false;
    }
    return true;
  }

  function resolveHoverTarget(eventTarget: EventTarget | null): {
    anchor: HTMLAnchorElement;
    ref: ResolvableItemReference;
  } | null {
    const anchor = findItemRefAnchor(eventTarget);
    if (!anchor || anchor.hasAttribute("download")) return null;
    if (popoverEl?.contains(anchor)) return null;
    const ref = resolveAnchorItemReference(anchor, getProviderContexts());
    if (!ref || isCurrentOpenItem(ref)) return null;
    const repos = getCachedRepositories();
    if (repos !== undefined && !isTrackedItemReference(ref, repos)) return null;
    return { anchor, ref };
  }

  function scheduleOpenForAnchor(
    anchor: HTMLAnchorElement,
    ref: ResolvableItemReference,
    pointer: { x: number; y: number } | null,
  ): void {
    lastPointer = pointer;

    if (anchor === activeAnchor) {
      clearCloseTimer();
      return;
    }
    if (anchor === pendingAnchor) return;

    clearCloseTimer();
    if (activeAnchor !== null) {
      hide();
      lastPointer = pointer;
    }
    clearOpenTimer();
    pendingAnchor = anchor;
    openTimer = setTimeout(() => {
      openTimer = undefined;
      if (pendingAnchor !== anchor) return;
      openForAnchor(anchor, ref);
    }, OPEN_DELAY_MS);
  }

  function handlePointerOver(event: Event): void {
    if (!(event instanceof PointerEvent) || event.pointerType === "touch") return;
    const resolved = resolveHoverTarget(event.target);
    if (!resolved) return;
    scheduleOpenForAnchor(resolved.anchor, resolved.ref, { x: event.clientX, y: event.clientY });
  }

  function handlePointerMove(event: Event): void {
    if (!(event instanceof PointerEvent) || event.pointerType === "touch") return;
    if (!pendingAnchor) return;
    if (event.target instanceof Node && pendingAnchor.contains(event.target)) {
      lastPointer = { x: event.clientX, y: event.clientY };
    }
  }

  function handlePointerOut(event: Event): void {
    if (!(event instanceof PointerEvent) || event.pointerType === "touch") return;
    const anchor = findItemRefAnchor(event.target);
    if (!anchor) return;
    const related = event.relatedTarget;
    if (related instanceof Node && anchor.contains(related)) return;
    if (anchor === pendingAnchor) {
      clearOpenTimer();
      pendingAnchor = null;
      return;
    }
    if (anchor === activeAnchor) {
      if (related instanceof Node && popoverEl?.contains(related)) return;
      scheduleClose();
    }
  }

  function handlePointerDown(event: Event): void {
    const anchor = findItemRefAnchor(event.target);
    if (!anchor || popoverEl?.contains(anchor)) return;
    hide();
  }

  function handleFocusIn(event: Event): void {
    if (!(event instanceof FocusEvent)) return;
    const targetEl = event.target;
    if (!(targetEl instanceof HTMLAnchorElement) || !targetEl.matches(":focus-visible")) return;
    const resolved = resolveHoverTarget(targetEl);
    if (!resolved) return;
    scheduleOpenForAnchor(resolved.anchor, resolved.ref, null);
  }

  function handleFocusOut(event: Event): void {
    if (!(event instanceof FocusEvent)) return;
    if (!activeAnchor && !pendingAnchor) return;
    const related = event.relatedTarget;
    if (related instanceof Node && (popoverEl?.contains(related) || activeAnchor?.contains(related))) return;
    hide();
  }

  function handleWindowKeydown(event: KeyboardEvent): void {
    if (event.key !== "Escape") return;
    if (visible) {
      event.preventDefault();
      event.stopPropagation();
      hide();
      return;
    }
    if (activeAnchor || pendingAnchor) {
      hide();
    }
  }

  function handlePopoverPointerEnter(): void {
    clearCloseTimer();
  }

  function handlePopoverPointerLeave(event: PointerEvent): void {
    if (event.pointerType === "touch") return;
    const related = event.relatedTarget;
    if (related instanceof Node && activeAnchor?.contains(related)) return;
    scheduleClose();
  }

  function portalToBody(node: HTMLElement): () => void {
    document.body.appendChild(node);
    return () => node.remove();
  }

  // Subscribe to the shared RepositoryReads snapshot once so untracked links
  // skip scheduling timers without making a network request.
  $effect(() => {
    const execution = untrack(() =>
      runtime.runCommand(
        Effect.gen(function* () {
          const reads = yield* RepositoryReads;
          getCachedRepositories = () => reads.snapshot;
        }),
        {
          operation: "observe repository catalog for item previews",
          safeContext: {},
          onFailure: () => {},
        },
      ),
    );
    return () => execution.interrupt();
  });

  // Attach delegated hover, focus, and capture-phase Escape listeners.
  $effect(() => {
    document.addEventListener("pointerover", handlePointerOver);
    document.addEventListener("pointermove", handlePointerMove);
    document.addEventListener("pointerout", handlePointerOut);
    document.addEventListener("pointerdown", handlePointerDown);
    document.addEventListener("focusin", handleFocusIn);
    document.addEventListener("focusout", handleFocusOut);
    window.addEventListener("keydown", handleWindowKeydown, true);
    return () => {
      hide();
      document.removeEventListener("pointerover", handlePointerOver);
      document.removeEventListener("pointermove", handlePointerMove);
      document.removeEventListener("pointerout", handlePointerOut);
      document.removeEventListener("pointerdown", handlePointerDown);
      document.removeEventListener("focusin", handleFocusIn);
      document.removeEventListener("focusout", handleFocusOut);
      window.removeEventListener("keydown", handleWindowKeydown, true);
    };
  });

  // Close on any in-app route change.
  let seenRoute: unknown = undefined;
  $effect(() => {
    const currentRoute = getRoute();
    if (seenRoute !== undefined && seenRoute !== currentRoute) {
      hide();
    }
    seenRoute = currentRoute;
  });

  // Keep the popover anchored while scrolling or resizing, and when its height
  // changes between loading and loaded states.
  $effect(() => {
    if (!visible || !popoverEl) return;
    void card;
    void position();
    const stopRepositioning = autoReposition(
      () => [popoverEl],
      () => void position(),
    );
    return () => stopRepositioning();
  });

  function pullState(pr: PullRequest): "open" | "draft" | "merged" | "closed" {
    if (pr.State === "merged") return "merged";
    if (pr.State === "closed") return "closed";
    if (pr.IsDraft) return "draft";
    return "open";
  }

  function reviewStatusChip(pr: PullRequest): { label: string; tone: "success" | "danger" } | null {
    if (pr.State !== "open") return null;
    const decision = (pr.ReviewDecision ?? "").trim().toUpperCase();
    if (decision === "APPROVED") return { label: "Approved", tone: "success" };
    if (decision === "CHANGES_REQUESTED") return { label: "Changes requested", tone: "danger" };
    return null;
  }

  function repoHeaderLabel(item: PullRequest | Issue): string {
    const path = item.repo.repo_path || `${item.repo_owner}/${item.repo_name}`;
    const host = item.repo.platform_host || item.platform_host;
    if (
      host &&
      providerUsesHostRoute({
        provider: item.repo.provider,
        platformHost: host,
        owner: item.repo.owner || item.repo_owner,
        name: item.repo.name || item.repo_name,
        repoPath: path,
      })
    ) {
      return `${host}/${path}`;
    }
    return path;
  }

  function itemTimestamp(preview: ItemPreview): string {
    if (preview.kind === "pr") {
      return preview.pull.MergedAt ?? preview.pull.ClosedAt ?? preview.pull.UpdatedAt;
    }
    return preview.issue.ClosedAt ?? preview.issue.UpdatedAt;
  }

  function routableItemForPreview(preview: ItemPreview): RoutableItemRef {
    const item = preview.kind === "pr" ? preview.pull : preview.issue;
    return {
      itemType: preview.kind,
      provider: item.repo.provider,
      platformHost: item.repo.platform_host || item.platform_host,
      repositoryKey: repositoryKeyFromWire(item.repo),
      owner: item.repo.owner || item.repo_owner,
      name: item.repo.name || item.repo_name,
      repoPath: item.repo.repo_path || `${item.repo_owner}/${item.repo_name}`,
      number: item.Number,
    };
  }

  function handleTitleClick(event: MouseEvent, preview: ItemPreview): void {
    const plainClick =
      event.button === 0 && !event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey;
    if (!plainClick) return;
    event.preventDefault();
    const item = routableItemForPreview(preview);
    hide();
    onOpenItem?.(item);
  }

  const loadedPreview = $derived(card?.kind === "loaded" ? card.preview : null);
  const loadedPull = $derived(loadedPreview?.kind === "pr" ? loadedPreview.pull : null);
  const loadedIssue = $derived(loadedPreview?.kind === "issue" ? loadedPreview.issue : null);

  const excerpt = $derived.by(() => {
    if (!loadedPreview) return "";
    const body = loadedPreview.kind === "pr" ? loadedPreview.pull.Body : loadedPreview.issue.Body;
    return summarizeMarkdownBody(body);
  });

  const parsedCI = $derived(loadedPull ? parseCIChecks(loadedPull.CIChecksJSON) : null);
  const bucketedCI = $derived(parsedCI ? bucketCIChecks(parsedCI.checks) : bucketCIChecks([]));
  const pullMerging = $derived(
    loadedPull
      ? isPullMerging(
          {
            provider: loadedPull.repo.provider,
            platformHost: loadedPull.repo.platform_host,
            repositoryKey: repositoryKeyFromWire(loadedPull.repo),
          },
          loadedPull.Number,
        )
      : false,
  );
  const mergeQueueLabel = $derived(
    loadedPull ? mergeQueueIndicatorLabel(loadedPull.merge_queue, loadedPull.merge_queue_removal) : undefined,
  );
  const visibleLabels = $derived.by(() => {
    const labels = loadedPull?.labels ?? loadedIssue?.labels ?? [];
    return {
      shown: labels.slice(0, MAX_VISIBLE_LABELS),
      overflow: Math.max(0, labels.length - MAX_VISIBLE_LABELS),
    };
  });
</script>

{#if visible && card}
  <div
    bind:this={popoverEl}
    id={popoverId}
    class="item-ref-popover kit-popover-card"
    role="group"
    aria-label="Item preview"
    data-testid="item-ref-popover"
    style={popoverStyle}
    onpointerenter={handlePopoverPointerEnter}
    onpointerleave={handlePopoverPointerLeave}
    {@attach portalToBody}
  >
    {#if card.kind === "loading"}
      <div class="item-ref-popover__status-row" role="status">
        <Spinner size={13} label="Loading preview" />
        <span class="item-ref-popover__status-ref">{card.ref.repoPath} #{card.ref.number}</span>
      </div>
    {:else if card.kind === "not-synced"}
      <div class="item-ref-popover__status-row">
        <span class="item-ref-popover__status-ref">{card.ref.repoPath} #{card.ref.number}</span>
        <span class="item-ref-popover__status-msg">Not synced in Forge. Click to open on the provider.</span>
      </div>
    {:else if card.kind === "not-found"}
      <div class="item-ref-popover__status-row">
        <span class="item-ref-popover__status-ref">{card.ref.repoPath} #{card.ref.number}</span>
        <span class="item-ref-popover__status-msg">Not found</span>
      </div>
    {:else if card.kind === "error"}
      <div class="item-ref-popover__status-row">
        <span class="item-ref-popover__status-ref">{card.ref.repoPath} #{card.ref.number}</span>
        <span class="item-ref-popover__status-msg">{card.message}</span>
      </div>
    {:else if loadedPreview}
      {@const item = loadedPreview.kind === "pr" ? loadedPreview.pull : loadedPreview.issue}
      {@const timestamp = itemTimestamp(loadedPreview)}
      {@const itemHref = buildItemReferenceHref({
        ...card.ref,
        itemType: loadedPreview.kind,
      })}

      <div class="item-ref-popover__eyebrow">
        <span class="item-ref-popover__repo">
          <span class="item-ref-popover__repo-name">{repoHeaderLabel(item)}</span>
          <RepoPrivateIcon isPrivate={item.repo.private} />
          <span class="item-ref-popover__number">#{item.Number}</span>
        </span>
        {#if timestamp}
          <time class="item-ref-popover__time" datetime={timestamp} title={localDateTimeLabel(timestamp)}>
            {formatRelativeTime(timestamp)}
          </time>
        {/if}
      </div>

      <a
        class="item-ref-popover__title"
        href={itemHref}
        onclick={(event) => handleTitleClick(event, loadedPreview)}
      >
        {item.Title}
      </a>

      {#if excerpt}
        <p class="item-ref-popover__excerpt">{excerpt}</p>
      {/if}

      {#if loadedPull && (loadedPull.HeadBranch || loadedPull.Additions > 0 || loadedPull.Deletions > 0)}
        <div class="item-ref-popover__code-row">
          {#if loadedPull.HeadBranch}
            <span
              class="item-ref-popover__branches"
              title={loadedPull.BaseBranch ? `${loadedPull.HeadBranch} -> ${loadedPull.BaseBranch}` : loadedPull.HeadBranch}
            >
              <span class="item-ref-popover__branch">{loadedPull.HeadBranch}</span>
              {#if loadedPull.BaseBranch}
                <span class="item-ref-popover__branch-arrow" aria-hidden="true">&rarr;</span>
                <span class="item-ref-popover__branch">{loadedPull.BaseBranch}</span>
              {/if}
            </span>
          {/if}
          {#if loadedPull.Additions > 0 || loadedPull.Deletions > 0}
            <span class="item-ref-popover__diff">
              <DiffStats additions={loadedPull.Additions} deletions={loadedPull.Deletions} />
            </span>
          {/if}
        </div>
      {/if}

      {#if visibleLabels.shown.length > 0}
        <div class="item-ref-popover__labels">
          {#each visibleLabels.shown as label (label.name)}
            <ColorLabel size="sm" name={label.name} color={label.color} />
          {/each}
          {#if visibleLabels.overflow > 0}
            <span class="item-ref-popover__label-more">+{visibleLabels.overflow}</span>
          {/if}
        </div>
      {/if}

      <div class="item-ref-popover__footer">
        <span class="item-ref-popover__footer-left">
          {#if loadedPull}
            {@const reviewChip = reviewStatusChip(loadedPull)}
            <ItemStateChip state={pullState(loadedPull)} size="xs" />
            {#if reviewChip}
              <Chip size="xs" tone={reviewChip.tone}>{reviewChip.label}</Chip>
            {/if}
            {#if loadedPull.State === "open" && loadedPull.MergeableState === "dirty"}
              <Chip size="xs" tone="warning">Conflicts</Chip>
            {/if}
            {#if loadedPull.State === "open" && loadedPull.merge_queue}
              <Chip size="xs" tone="warning">
                {hasMergeQueuePosition(loadedPull.merge_queue)
                  ? `#${loadedPull.merge_queue.position} in queue`
                  : "Queued"}
              </Chip>
            {:else if loadedPull.State === "open" && loadedPull.merge_queue_removal}
              <Chip size="xs" tone="danger">Removed from queue</Chip>
            {/if}
          {:else if loadedIssue}
            <ItemStateChip state={loadedIssue.State} size="xs" />
          {/if}
          {#if item.Author}
            <span class="item-ref-popover__author">{item.Author}</span>
          {/if}
        </span>

        <span class="item-ref-popover__footer-right">
          {#if loadedPull}
            {#if loadedPull.workspace}
              <WorkspaceIndicator status={loadedPull.workspace.status} />
            {/if}
            <PullStatusTokens
              reviewDecision=""
              merging={pullMerging}
              mergeAfterCI={loadedPull.deferred_merge_pending === true}
              mergeQueueLabel={loadedPull.State === "open" ? undefined : mergeQueueLabel}
              mergeQueueRemoved={!loadedPull.merge_queue}
              stack={loadedPull.stack}
              bucketed={bucketedCI}
              ciError={parsedCI && parsedCI.error !== null ? safeDiagnosticText(parsedCI.error) : null}
              conflict={false}
              commentCount={loadedPull.CommentCount}
            />
          {:else if loadedIssue}
            {#if loadedIssue.workspace}
              <WorkspaceIndicator status={loadedIssue.workspace.status} />
            {/if}
            <PullStatusTokens bucketed={bucketedCI} commentCount={loadedIssue.CommentCount} />
          {/if}
        </span>
      </div>
    {/if}
  </div>
{/if}

<style>
  .item-ref-popover {
    position: fixed;
    z-index: var(--z-tooltip);
    width: min(372px, calc(100vw - 16px));
    padding: var(--space-4) var(--space-5);
    display: grid;
    gap: var(--space-2);
    font-size: var(--font-size-sm);
    line-height: 1.4;
    overflow-wrap: break-word;
    animation: item-ref-popover-in 0.12s ease-out;
  }

  @media (prefers-reduced-motion: reduce) {
    .item-ref-popover {
      animation: none;
    }
  }

  @keyframes item-ref-popover-in {
    from {
      opacity: 0;
      transform: translateY(2px);
    }
    to {
      opacity: 1;
      transform: translateY(0);
    }
  }

  .item-ref-popover__status-row {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
  }

  .item-ref-popover__status-ref {
    font-weight: 500;
    color: var(--text-primary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .item-ref-popover__status-msg {
    color: var(--text-muted);
    margin-left: auto;
    flex-shrink: 0;
  }

  .item-ref-popover__eyebrow {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-3);
    font-size: var(--font-size-xs);
    color: var(--text-muted);
    line-height: 1.3;
  }

  .item-ref-popover__repo {
    display: inline-flex;
    align-items: center;
    gap: var(--space-1);
    min-width: 0;
  }

  .item-ref-popover__repo-name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .item-ref-popover__number {
    flex-shrink: 0;
    font-variant-numeric: tabular-nums;
  }

  .item-ref-popover__time {
    flex-shrink: 0;
    font-variant-numeric: tabular-nums;
  }

  .item-ref-popover__title {
    color: var(--text-primary);
    font-size: var(--font-size-md);
    font-weight: 600;
    line-height: 1.35;
    text-decoration: none;
    display: -webkit-box;
    -webkit-box-orient: vertical;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    overflow: hidden;
  }

  .item-ref-popover__title:hover {
    color: var(--accent-blue);
    text-decoration: underline;
  }

  .item-ref-popover__excerpt {
    margin: 0;
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    line-height: 1.45;
    display: -webkit-box;
    -webkit-box-orient: vertical;
    -webkit-line-clamp: 3;
    line-clamp: 3;
    overflow: hidden;
  }

  .item-ref-popover__code-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-3);
    min-width: 0;
  }

  .item-ref-popover__branches {
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    min-width: 0;
    font-family: var(--font-mono);
    font-size: var(--font-size-2xs);
    color: var(--text-muted);
  }

  .item-ref-popover__branch {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 140px;
  }

  .item-ref-popover__branch-arrow {
    flex-shrink: 0;
  }

  .item-ref-popover__diff {
    flex-shrink: 0;
    margin-left: auto;
    font-size: var(--font-size-xs);
  }

  .item-ref-popover__labels {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-2);
  }

  .item-ref-popover__label-more {
    color: var(--text-muted);
    font-size: var(--font-size-2xs);
    font-variant-numeric: tabular-nums;
  }

  .item-ref-popover__footer {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-2) var(--space-3);
    padding-top: var(--space-2);
    border-top: 1px solid var(--border-muted);
  }

  .item-ref-popover__footer-left {
    display: inline-flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-2);
    min-width: 0;
  }

  .item-ref-popover__author {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 120px;
  }

  .item-ref-popover__footer-right {
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    flex-shrink: 0;
    margin-left: auto;
  }
</style>
