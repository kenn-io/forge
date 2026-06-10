<script lang="ts">
  import CalendarIcon from "@lucide/svelte/icons/calendar";
  import ChevronLeftIcon from "@lucide/svelte/icons/chevron-left";
  import ChevronRightIcon from "@lucide/svelte/icons/chevron-right";
  import XIcon from "@lucide/svelte/icons/x";

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

  const today = new Date();
  let open = $state(false);
  let rootEl = $state<HTMLDivElement>();
  let buttonEl = $state<HTMLButtonElement>();
  let viewYear = $state(today.getFullYear());
  let viewMonth = $state(today.getMonth());

  const weekdays = ["Mo", "Tu", "We", "Th", "Fr", "Sa", "Su"];
  const popoverID = `date-picker-${Math.random().toString(36).slice(2)}`;

  const displayValue = $derived(value ? formatDate(value) : placeholder);
  const monthLabel = $derived(new Date(viewYear, viewMonth, 1).toLocaleDateString(undefined, {
    month: "long",
    year: "numeric",
  }));
  const calendarDays = $derived(buildCalendarDays(viewYear, viewMonth));

  $effect(() => {
    if (!value) return;
    const next = initialDate(value);
    viewYear = next.getFullYear();
    viewMonth = next.getMonth();
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

  function toISO(date: Date): string {
    const year = date.getFullYear();
    const month = String(date.getMonth() + 1).padStart(2, "0");
    const day = String(date.getDate()).padStart(2, "0");
    return `${year}-${month}-${day}`;
  }

  function formatDate(input: string): string {
    const date = initialDate(input);
    return date.toLocaleDateString(undefined, {
      month: "short",
      day: "numeric",
      year: new Date().getFullYear() === date.getFullYear() ? undefined : "numeric",
    });
  }

  function buildCalendarDays(year: number, month: number): Date[] {
    const first = new Date(year, month, 1);
    const startOffset = (first.getDay() + 6) % 7;
    const start = new Date(year, month, 1 - startOffset);
    return Array.from({ length: 42 }, (_, index) => new Date(start.getFullYear(), start.getMonth(), start.getDate() + index));
  }

  function shiftMonth(delta: number): void {
    const next = new Date(viewYear, viewMonth + delta, 1);
    viewYear = next.getFullYear();
    viewMonth = next.getMonth();
  }

  function pick(date: Date): void {
    onchange(toISO(date));
    open = false;
    buttonEl?.focus();
  }

  function clearDate(event: MouseEvent): void {
    event.stopPropagation();
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
      class="date-picker-popover"
      role="dialog"
      aria-label={ariaLabel}
      tabindex="-1"
      onkeydown={handleDatePickerKeydown}
    >
      <div class="date-picker-header">
        <button type="button" class="date-picker-nav" aria-label="Previous month" onclick={() => shiftMonth(-1)}>
          <ChevronLeftIcon size="14" strokeWidth="2" aria-hidden="true" />
        </button>
        <span>{monthLabel}</span>
        <button type="button" class="date-picker-nav" aria-label="Next month" onclick={() => shiftMonth(1)}>
          <ChevronRightIcon size="14" strokeWidth="2" aria-hidden="true" />
        </button>
      </div>
      <div class="date-picker-grid" role="grid" aria-label={monthLabel}>
        {#each weekdays as day (day)}
          <span class="date-picker-weekday">{day}</span>
        {/each}
        {#each calendarDays as day (toISO(day))}
          <button
            type="button"
            class="date-picker-day"
            class:outside={day.getMonth() !== viewMonth}
            class:selected={toISO(day) === value}
            aria-label={day.toLocaleDateString(undefined, { weekday: "long", month: "long", day: "numeric", year: "numeric" })}
            onclick={() => pick(day)}
          >
            {day.getDate()}
          </button>
        {/each}
      </div>
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
    z-index: 120;
    top: calc(100% + 3px);
    left: 0;
    width: 224px;
    padding: 8px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    background: var(--bg-surface);
    box-shadow: var(--shadow-md);
  }

  .date-picker-header {
    display: grid;
    grid-template-columns: 28px 1fr 28px;
    align-items: center;
    gap: 4px;
    margin-bottom: 6px;
    color: var(--text-primary);
    font-size: var(--font-size-xs);
    font-weight: 700;
    text-align: center;
  }

  .date-picker-nav {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 26px;
    height: 24px;
    border-radius: var(--radius-sm);
    color: var(--text-muted);
  }

  .date-picker-nav:hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .date-picker-grid {
    display: grid;
    grid-template-columns: repeat(7, 1fr);
    gap: 2px;
  }

  .date-picker-weekday,
  .date-picker-day {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    height: 24px;
    border-radius: var(--radius-sm);
    font-size: var(--font-size-2xs);
    line-height: 1;
  }

  .date-picker-weekday {
    color: var(--text-muted);
    font-weight: 700;
  }

  .date-picker-day {
    color: var(--text-secondary);
  }

  .date-picker-day:hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .date-picker-day.outside {
    color: var(--text-faint);
    opacity: 0.7;
  }

  .date-picker-day.selected {
    background: var(--accent-blue);
    color: #fff;
    font-weight: 700;
  }
</style>
