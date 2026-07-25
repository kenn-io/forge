<script lang="ts">
  interface Props {
    owner: string;
    name: string;
    number: number;
    initialScrollTop?: number;
    onScrollTopChange?: (scrollTop: number) => void;
  }

  const { owner, name, number, initialScrollTop = 0, onScrollTopChange }: Props = $props();
</script>

<!-- The real layout restores initialScrollTop and reports scrolls back; the
     double exposes both so the view's scroll-memory wiring is observable. -->
<div
  data-testid="diff-files"
  data-initial-scroll-top={String(initialScrollTop)}
  role="presentation"
  onscroll={(event) => onScrollTopChange?.((event.currentTarget as HTMLElement).scrollTop)}
>
  Files changed {owner}/{name}#{number}
</div>
