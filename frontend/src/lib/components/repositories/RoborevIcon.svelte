<!-- Canonical Roborev favicon geometry, adapted to the surrounding neutral icon color. -->
<script lang="ts">
  interface Props {
    size?: number;
  }

  let { size = 16 }: Props = $props();
</script>

<svg
  xmlns="http://www.w3.org/2000/svg"
  viewBox="0 0 32 32"
  width={size}
  height={size}
  fill="none"
  aria-hidden="true"
>
  <circle cx="13" cy="13" r="10" stroke="currentColor" stroke-width="2" />
  <rect x="21" y="21" width="10" height="3" fill="currentColor" transform="rotate(45 21 21)" />
  <rect x="9" y="11" width="3" height="3" fill="currentColor" />
  <rect x="14" y="11" width="3" height="3" fill="currentColor" />
</svg>
