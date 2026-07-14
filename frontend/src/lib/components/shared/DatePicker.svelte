<script lang="ts">
  import CalendarIcon from "@lucide/svelte/icons/calendar";
  import XIcon from "@lucide/svelte/icons/x";
  import { Calendar, todayStr } from "@kenn-io/kit-ui";

  interface Props {
    value: string;
    onchange: (value: string) => void;
    ariaLabel?: string;
    placeholder?: string;
    disabled?: boolean;
    clearable?: boolean;
    clearLabel?: string;
    onEscape?: () => void;
    class?: string;
  }

  let {
    value,
    onchange,
    ariaLabel = "Choose date",
    placeholder = "Pick date",
    disabled = false,
    clearable = false,
    clearLabel,
    onEscape,
    class: className = "",
  }: Props = $props();

  let open = $state(false);
  let rootEl = $state<HTMLDivElement>();
  let buttonEl = $state<HTMLButtonElement>();
  let calendarMonth = $state(todayStr());

  const popoverID = `date-picker-${Math.random().toString(36).slice(2)}`;
  const displayValue = $derived(value ? formatDate(value) : placeholder);

  $effect(() => {
    if (/^\d{4}-\d{2}-\d{2}$/.test(value)) calendarMonth = value;
  });

  $effect(() => {
    if (!open) return;

    function handleMousedown(event: MouseEvent): void {
      const target = event.target as Node;
      if (rootEl?.contains(target)) return;
      open = false;
    }

    function handleKeydown(event: KeyboardEvent): void {
      if (event.key === "Escape") {
        open = false;
        buttonEl?.focus();
      }
    }

    document.addEventListener("mousedown", handleMousedown);
    document.addEventListener("keydown", handleKeydown);
    return () => {
      document.removeEventListener("mousedown", handleMousedown);
      document.removeEventListener("keydown", handleKeydown);
    };
  });

  function initialDate(input: string): Date {
    if (/^\d{4}-\d{2}-\d{2}$/.test(input)) {
      const [year, month, day] = input.split("-").map(Number);
      return new Date(year!, month! - 1, day!);
    }
    return new Date();
  }

  function formatDate(input: string): string {
    const date = initialDate(input);
    return date.toLocaleDateString(undefined, {
      month: "short",
      day: "numeric",
      year: new Date().getFullYear() === date.getFullYear() ? undefined : "numeric",
    });
  }

  function pick(date: string): void {
    onchange(date);
    open = false;
    buttonEl?.focus();
  }

  function clearDate(): void {
    onchange("");
    open = false;
    buttonEl?.focus();
  }

  function handleDatePickerKeydown(event: KeyboardEvent): void {
    if (event.key !== "Escape") return;
    if (!open && !onEscape) return;
    event.preventDefault();
    event.stopPropagation();
    open = false;
    buttonEl?.focus();
    onEscape?.();
  }
</script>

<div class={["date-picker", className]} bind:this={rootEl}>
  <button
    bind:this={buttonEl}
    class="date-picker-trigger"
    type="button"
    onclick={() => {
      if (!disabled) open = !open;
    }}
    onkeydown={handleDatePickerKeydown}
    aria-haspopup="dialog"
    aria-expanded={open}
    aria-controls={popoverID}
    aria-label={`${ariaLabel}: ${displayValue}`}
    {disabled}
  >
    <CalendarIcon size="13" strokeWidth="1.9" aria-hidden="true" />
    <span class:placeholder={!value}>{displayValue}</span>
  </button>

  {#if clearable && value}
    <button
      class="date-picker-clear"
      type="button"
      aria-label={clearLabel ?? `Clear ${ariaLabel.toLowerCase()}`}
      onclick={clearDate}
      onkeydown={handleDatePickerKeydown}
      {disabled}
    >
      <XIcon size="12" strokeWidth="2" aria-hidden="true" />
    </button>
  {/if}

  {#if open}
    <div
      id={popoverID}
      class="date-picker-popover kit-popover-card"
      role="dialog"
      aria-label={ariaLabel}
      tabindex="-1"
      onkeydown={handleDatePickerKeydown}
    >
      <Calendar
        bind:month={calendarMonth}
        selected={value ? { from: value, to: value } : null}
        onpick={pick}
      />
    </div>
  {/if}
</div>

<style>
  .date-picker {
    position: relative;
    display: inline-flex;
    align-items: center;
    gap: 2px;
    min-width: 136px;
  }

  /* kit-ui-check-ignore: popup trigger needs native button focus ownership plus aria-haspopup, aria-expanded, aria-controls, and Escape propagation; kit Button cannot preserve the full composite contract */
  .date-picker-trigger {
    box-sizing: border-box;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    width: 100%;
    height: 26px;
    padding: 0 8px;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    background: var(--bg-inset);
    color: var(--text-secondary);
    font-family: inherit;
    font-size: var(--font-size-xs);
    font-weight: 600;
    text-align: left;
  }

  .date-picker-trigger:hover:not(:disabled),
  .date-picker-trigger[aria-expanded="true"] {
    border-color: var(--border-default);
    color: var(--text-primary);
  }

  .date-picker-trigger:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .date-picker-trigger > span {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .placeholder {
    color: var(--text-muted);
  }

  /* kit-ui-check-ignore: native clear control participates in the DatePicker Escape/focus composite; kit IconButton cannot forward the required keydown handler */
  .date-picker-clear {
    flex: 0 0 auto;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 22px;
    height: 26px;
    border-radius: var(--radius-sm);
    color: var(--text-muted);
    border: 1px solid var(--border-muted);
    background: var(--bg-inset);
  }

  .date-picker-clear:hover {
    background: var(--bg-surface-hover);
    color: var(--accent-red);
  }

  .date-picker-popover {
    position: absolute;
    z-index: 94;
    top: calc(100% + 3px);
    left: 0;
    width: max-content;
  }
</style>
