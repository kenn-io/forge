<script lang="ts">
  import { SearchInput } from "@kenn-io/kit-ui";
  import { searchQueryOperators } from "../../utils/search-query.js";

  // Search field for list queries that understand negation. A native input
  // cannot color part of its text, so while the query contains an operator
  // ("!", uppercase "NOT") a layer redraws the whole query over the input with
  // operators in the accent color, and the input's own text turns transparent
  // while it keeps the caret, selection, and all interaction.

  interface Props {
    value?: string;
    placeholder?: string;
    size?: "sm" | "md";
    block?: boolean;
    ariaLabel?: string;
    oninput?: (value: string) => void;
  }

  let {
    value = $bindable(""),
    placeholder = "Search…",
    size = "md",
    block = false,
    ariaLabel = "Search",
    oninput = undefined,
  }: Props = $props();

  let inputEl = $state<HTMLInputElement | undefined>(undefined);
  let geometry = $state({ left: 0, top: 0, width: 0, height: 0 });
  let scrollLeft = $state(0);

  type Segment = { start: number; text: string; operator: boolean };

  const segments = $derived.by((): Segment[] => {
    const result: Segment[] = [];
    let cursor = 0;
    for (const range of searchQueryOperators(value)) {
      if (range.start > cursor) {
        result.push({ start: cursor, text: value.slice(cursor, range.start), operator: false });
      }
      result.push({ start: range.start, text: value.slice(range.start, range.end), operator: true });
      cursor = range.end;
    }
    if (cursor < value.length) {
      result.push({ start: cursor, text: value.slice(cursor), operator: false });
    }
    return result;
  });
  const hasOperators = $derived(segments.some((segment) => segment.operator));

  // Moves the layer into the kit field wrapper, directly behind the input, and
  // keeps its box and horizontal scroll aligned with the input's text area.
  function trackInput(layer: HTMLElement): (() => void) | undefined {
    const input = inputEl;
    const field = input?.parentElement;
    if (!input || !field) return undefined;
    field.insertBefore(layer, input);

    const measure = () => {
      geometry = {
        left: input.offsetLeft,
        top: input.offsetTop,
        width: input.clientWidth,
        height: input.offsetHeight,
      };
      scrollLeft = input.scrollLeft;
    };
    // Caret moves can scroll a long query without an input event.
    const syncScroll = () => requestAnimationFrame(() => (scrollLeft = input.scrollLeft));
    const scrollEvents = ["input", "scroll", "keyup", "select", "focus", "blur", "pointerup"];
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(input);
    for (const type of scrollEvents) input.addEventListener(type, syncScroll);
    return () => {
      observer.disconnect();
      for (const type of scrollEvents) input.removeEventListener(type, syncScroll);
      layer.remove();
    };
  }
</script>

<SearchInput
  bind:value
  bind:inputEl
  {placeholder}
  {size}
  {block}
  {ariaLabel}
  oninput={(next) => oninput?.(next)}
  class={hasOperators ? "query-field query-field--operators" : "query-field"}
/>
<!-- The #if keeps a comment anchor in this component's DOM range, so moving
     the layer into the kit field cannot confuse Svelte's sibling teardown. -->
{#if inputEl}
  <div
    class="query-field__layer"
    aria-hidden="true"
    hidden={!hasOperators}
    style:left="{geometry.left}px"
    style:top="{geometry.top}px"
    style:width="{geometry.width}px"
    style:height="{geometry.height}px"
    {@attach trackInput}
  >
    <span class="query-field__text" style:transform="translateX({-scrollLeft}px)"
      >{#each segments as segment (segment.start)}{#if segment.operator}<span
            class="query-field__operator">{segment.text}</span
          >{:else}{segment.text}{/if}{/each}</span
    >
  </div>
{/if}

<style>
  :global(.query-field) {
    position: relative;
  }

  /* The layer draws the visible text; the input keeps a visible caret, and its
   * selection highlight shows through beneath the layer's glyphs. */
  :global(.query-field--operators .kit-text-input__control) {
    color: transparent;
    caret-color: var(--text-primary);
  }

  .query-field__layer {
    position: absolute;
    display: flex;
    align-items: center;
    overflow: hidden;
    pointer-events: none;
  }

  .query-field__layer[hidden] {
    display: none;
  }

  .query-field__text {
    white-space: pre;
    line-height: normal;
    /* Inputs reset text-rendering to auto; the page default would otherwise
     * be inherited here and could kern the layer differently. */
    text-rendering: auto;
  }

  .query-field__operator {
    color: var(--accent-blue);
  }
</style>
