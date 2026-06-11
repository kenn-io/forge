<script lang="ts">
  import { tick, type Snippet } from "svelte";
  import ActionButton from "../shared/ActionButton.svelte";
  import UserPicker from "./UserPicker.svelte";
  import { floatingPopoverStyle } from "../shared/floatingPosition.js";

  interface Props {
    label: string;
    users: string[];
    canEdit?: boolean;
    disabled?: boolean;
    loadCandidates: () => Promise<string[]>;
    onchange: (next: string[]) => Promise<unknown>;
    icon?: Snippet;
  }

  const {
    label,
    users,
    canEdit = false,
    disabled = false,
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
  let anchorEl = $state<HTMLDivElement>();
  let buttonEl = $state<HTMLSpanElement>();
  let popoverEl = $state<HTMLDivElement>();
  let popoverStyle = $state("");

  const editorId = $derived(label.toLowerCase().replace(/\s+/g, "-"));

  function closePicker(): void {
    open = false;
    pendingUser = null;
    pickerError = null;
  }

  function positionPicker(): void {
    // Anchor under the trigger button itself, left-aligned like a
    // conventional dropdown menu. The editor row sits on the left side
    // of the page, so end-alignment would float the panel away from
    // its trigger.
    const trigger = buttonEl ?? anchorEl;
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
  <div class="user-list-editor" bind:this={anchorEl} data-user-list-editor={editorId}>
    {#if users.length > 0}
      <span class="user-list-editor__summary" title="{label}: {users.join(', ')}">
        <span class="user-list-editor__label">{label}:</span>
        {users.join(", ")}
      </span>
    {/if}
    {#if canEdit}
      <span class="user-list-editor__button" bind:this={buttonEl}>
        <ActionButton
          class="btn--user-list"
          {label}
          shortLabel={label}
          ariaLabel="Edit {label.toLowerCase()}"
          size="sm"
          surface="soft"
          tone="neutral"
          {disabled}
          ariaExpanded={open}
          onclick={togglePicker}
        >
          {#if icon}{@render icon()}{/if}
        </ActionButton>
      </span>
    {/if}
  </div>
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
    align-items: center;
    gap: 6px;
    min-width: 0;
  }

  .user-list-editor__summary {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
  }

  .user-list-editor__label {
    color: var(--text-muted);
  }

  .user-list-editor__button {
    display: inline-flex;
  }

  .user-list-editor__popover {
    position: fixed;
    z-index: 60;
  }
</style>
