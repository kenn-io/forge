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
    /// Extra context appended to the chip tooltip, e.g. provider caveats.
    tooltipNote?: string;
    loadCandidates: () => Promise<string[]>;
    onchange: (next: string[]) => Promise<unknown>;
    icon?: Snippet;
  }

  const {
    label,
    users,
    canEdit = false,
    disabled = false,
    tooltipNote = undefined,
    loadCandidates,
    onchange,
    icon = undefined,
  }: Props = $props();

  let open = $state(false);
  let candidates = $state<string[]>([]);
  let candidatesLoading = $state(false);
  let pendingUser = $state<string | null>(null);
  let pickerError = $state<string | null>(null);
  let autofocusFilter = $state(false);
  let anchorEl = $state<HTMLSpanElement>();
  let popoverEl = $state<HTMLDivElement>();
  let popoverStyle = $state("");

  const editorId = $derived(label.toLowerCase().replace(/\s+/g, "-"));
  const chipTitle = $derived.by(() => {
    const base = users.length > 0 ? `${label}: ${users.join(", ")}` : `Add ${label.toLowerCase()}`;
    return tooltipNote ? `${base}\n${tooltipNote}` : base;
  });

  function closePicker(): void {
    open = false;
    pendingUser = null;
    pickerError = null;
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

  async function togglePicker(event?: MouseEvent): Promise<void> {
    if (open) {
      closePicker();
      return;
    }
    autofocusFilter = event !== undefined && !(window.matchMedia?.("(pointer: coarse)").matches ?? false);
    open = true;
    pickerError = null;
    candidatesLoading = true;
    await tick();
    positionPicker();
    try {
      candidates = await loadCandidates();
      void tick().then(() => {
        if (open) positionPicker();
      });
    } catch (err) {
      pickerError = err instanceof Error ? err.message : String(err);
    } finally {
      candidatesLoading = false;
    }
  }

  async function toggleUser(username: string): Promise<void> {
    if (pendingUser !== null) return;
    pendingUser = username;
    pickerError = null;
    const key = username.toLowerCase();
    const next = users.some((user) => user.toLowerCase() === key)
      ? users.filter((user) => user.toLowerCase() !== key)
      : [...users, username];
    try {
      await onchange(next);
    } catch (err) {
      pickerError = err instanceof Error ? err.message : String(err);
    } finally {
      pendingUser = null;
    }
  }

  async function clearUsers(): Promise<void> {
    if (pendingUser !== null || users.length === 0) return;
    pendingUser = "";
    pickerError = null;
    try {
      await onchange([]);
    } catch (err) {
      pickerError = err instanceof Error ? err.message : String(err);
    } finally {
      pendingUser = null;
    }
  }

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
        {pendingUser}
        error={pickerError}
        {autofocusFilter}
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
