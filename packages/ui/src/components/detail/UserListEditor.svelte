<script module lang="ts">
  // All editor instances share one open-picker slot so at most one
  // picker exists across the app. The document-mousedown dismissal
  // below only sees pointer presses; keyboard or assistive-tech
  // activation of another chip reaches togglePicker without any
  // mousedown, and this slot is what closes the previous picker then.
  let closeOpenEditor: (() => void) | null = null;
</script>

<script lang="ts">
  import { tick, type Snippet } from "svelte";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import Chip from "../shared/Chip.svelte";
  import UserPicker from "./UserPicker.svelte";
  import { floatingPopoverStyle } from "../shared/floatingPosition.js";

  interface Props {
    label: string;
    users: string[];
    canEdit?: boolean;
    disabled?: boolean;
    /// Shown as the chip tooltip while disabled — the actionable
    /// reason (missing write credential, rate limit) the server
    /// reported for this operation.
    disabledReason?: string | undefined;
    /// Extra context appended to the chip tooltip, e.g. provider caveats.
    tooltipNote?: string;
    /// Returns candidate usernames matching the filter query. Called
    /// with "" when the picker opens and again as the user types, so
    /// candidates beyond the first page stay reachable by searching.
    loadCandidates: (query: string) => Promise<string[]>;
    avatarUrlForUser?: ((username: string) => string) | undefined;
    onchange: (next: string[]) => Promise<unknown>;
    icon?: Snippet;
  }

  const {
    label,
    users,
    canEdit = false,
    disabled = false,
    disabledReason = undefined,
    tooltipNote = undefined,
    loadCandidates,
    avatarUrlForUser = undefined,
    onchange,
    icon = undefined,
  }: Props = $props();

  let open = $state(false);
  let candidates = $state<string[]>([]);
  let candidatesQuery = $state("");
  let candidatesLoading = $state(false);
  let pendingUser = $state<string | null>(null);
  // Candidate-fetch and mutation failures are tracked separately so a
  // late-resolving fetch success cannot erase the error from a save
  // that genuinely failed. The mutation error wins when both are set.
  let candidatesError = $state<string | null>(null);
  let mutationError = $state<string | null>(null);
  let autofocusFilter = $state(false);
  let anchorEl = $state<HTMLSpanElement>();
  let popoverEl = $state<HTMLDivElement>();
  let popoverStyle = $state("");

  const editorId = $derived(label.toLowerCase().replace(/\s+/g, "-"));
  const chipTitle = $derived.by(() => {
    if (disabled && disabledReason !== undefined) return disabledReason;
    const base = users.length > 0 ? `${label}: ${users.join(", ")}` : `Add ${label.toLowerCase()}`;
    return tooltipNote ? `${base}\n${tooltipNote}` : base;
  });

  let candidateFetchSeq = 0;
  let queryDebounce: ReturnType<typeof setTimeout> | null = null;

  function closePicker(): void {
    if (closeOpenEditor === closePicker) closeOpenEditor = null;
    open = false;
    pendingUser = null;
    candidatesError = null;
    mutationError = null;
    if (queryDebounce !== null) {
      clearTimeout(queryDebounce);
      queryDebounce = null;
    }
  }

  async function fetchCandidates(query: string): Promise<void> {
    const seq = ++candidateFetchSeq;
    candidatesLoading = true;
    try {
      const next = await loadCandidates(query);
      if (seq !== candidateFetchSeq) return;
      candidates = next;
      candidatesQuery = query;
      // A fresh successful fetch supersedes any candidate-load error a
      // previous request left behind; mutation errors stay visible.
      candidatesError = null;
      void tick().then(() => {
        if (open) positionPicker();
      });
    } catch (err) {
      if (seq === candidateFetchSeq) {
        candidatesError = err instanceof Error ? err.message : String(err);
      }
    } finally {
      if (seq === candidateFetchSeq) {
        candidatesLoading = false;
      }
    }
  }

  function onPickerQuery(query: string): void {
    if (queryDebounce !== null) clearTimeout(queryDebounce);
    queryDebounce = setTimeout(() => {
      queryDebounce = null;
      void fetchCandidates(query);
    }, 200);
  }

  function positionPicker(): void {
    // Anchor under the chip, left-aligned like a conventional
    // dropdown menu. The chips row sits on the left side of the page,
    // so end-alignment would float the panel away from its trigger.
    const trigger = anchorEl;
    if (!trigger) return;
    const popoverHeight = popoverEl?.getBoundingClientRect().height;
    popoverStyle = floatingPopoverStyle({
      trigger: trigger.getBoundingClientRect(),
      viewportWidth: window.innerWidth,
      viewportHeight: window.innerHeight,
      ...(popoverHeight !== undefined ? { popoverHeight } : {}),
      align: "start",
      edgeGap: 12,
      maxWidth: 260,
      constrainWidth: true,
    });
  }

  function onDocumentMousedown(event: MouseEvent): void {
    // Dismiss on any press outside the chip and panel. Pressing
    // another editor's chip lands here too, so opening one picker
    // closes any other before its own toggle runs.
    if (!open) return;
    const target = event.target as Node;
    if (anchorEl?.contains(target) || popoverEl?.contains(target)) return;
    closePicker();
  }

  async function togglePicker(event?: MouseEvent): Promise<void> {
    if (open) {
      closePicker();
      return;
    }
    autofocusFilter = event !== undefined && !(window.matchMedia?.("(pointer: coarse)").matches ?? false);
    closeOpenEditor?.();
    closeOpenEditor = closePicker;
    open = true;
    candidatesError = null;
    mutationError = null;
    await tick();
    positionPicker();
    await fetchCandidates("");
  }

  async function toggleUser(username: string): Promise<void> {
    // The chip honors disabled, but the picker can already be open
    // when the item goes stale; never mutate from a stale view.
    if (disabled || !canEdit) return;
    if (pendingUser !== null) return;
    pendingUser = username;
    mutationError = null;
    const key = username.toLowerCase();
    const next = users.some((user) => user.toLowerCase() === key)
      ? users.filter((user) => user.toLowerCase() !== key)
      : [...users, username];
    try {
      await onchange(next);
    } catch (err) {
      mutationError = err instanceof Error ? err.message : String(err);
    } finally {
      pendingUser = null;
    }
  }

  async function clearUsers(): Promise<void> {
    if (disabled || !canEdit) return;
    if (pendingUser !== null || users.length === 0) return;
    pendingUser = "";
    mutationError = null;
    try {
      await onchange([]);
    } catch (err) {
      mutationError = err instanceof Error ? err.message : String(err);
    } finally {
      pendingUser = null;
    }
  }

  $effect(() => {
    // A view that goes stale while the picker is open must not keep
    // an actionable picker on screen: its mutations would target
    // whatever item the parent handlers now point at.
    if (disabled && open) {
      closePicker();
    }
  });

  $effect(() => {
    if (!open) return;

    function updatePosition(): void {
      positionPicker();
    }

    window.addEventListener("resize", updatePosition);
    window.addEventListener("scroll", updatePosition, true);
    return () => {
      window.removeEventListener("resize", updatePosition);
      window.removeEventListener("scroll", updatePosition, true);
    };
  });
</script>

<svelte:document onmousedown={onDocumentMousedown} />

{#if users.length > 0 || canEdit}
  <span class="user-list-editor" bind:this={anchorEl} data-user-list-editor={editorId}>
    {#if canEdit}
      <Chip
        interactive
        size="md"
        tone={users.length > 0 ? "neutral" : "muted"}
        uppercase={false}
        title={chipTitle}
        ariaLabel="Edit {label.toLowerCase()}"
        expanded={open}
        {disabled}
        class="user-list-editor__chip"
        onclick={togglePicker}
      >
        {#if icon}{@render icon()}{/if}
        {#if users.length > 0}
          <span class="user-list-editor__names">{users.join(", ")}</span>
        {:else}
          <PlusIcon size={11} strokeWidth={2.4} aria-hidden="true" />
        {/if}
      </Chip>
    {:else}
      <Chip
        size="md"
        tone="neutral"
        uppercase={false}
        title={chipTitle}
        class="user-list-editor__chip"
      >
        {#if icon}{@render icon()}{/if}
        <span class="user-list-editor__names">{users.join(", ")}</span>
      </Chip>
    {/if}
  </span>
  {#if open}
    <div class="user-list-editor__popover" style={popoverStyle} bind:this={popoverEl}>
      <UserPicker
        title="Edit {label.toLowerCase()}"
        {candidates}
        selected={users}
        loading={candidatesLoading}
        {candidatesQuery}
        {pendingUser}
        error={mutationError ?? candidatesError}
        {autofocusFilter}
        {avatarUrlForUser}
        onquery={onPickerQuery}
        ontoggle={toggleUser}
        onclear={clearUsers}
        onclose={closePicker}
      />
    </div>
  {/if}
{/if}

<style>
  .user-list-editor {
    display: inline-flex;
    min-width: 0;
  }

  .user-list-editor :global(.user-list-editor__chip) {
    max-width: 220px;
    font-weight: 500;
  }

  .user-list-editor__names {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .user-list-editor__popover {
    position: fixed;
    z-index: 60;
  }
</style>
