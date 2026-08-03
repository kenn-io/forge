<!-- Canonical Roborev favicon mark: https://roborev.io/assets/static/favicon.svg -->
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
  <rect width="32" height="32" fill="#0a0a0a" />
  <circle cx="13" cy="13" r="10" stroke="#00ff9f" stroke-width="2" fill="#0a0a0a" />
  <rect x="21" y="21" width="10" height="3" fill="#00ff9f" transform="rotate(45 21 21)" />
  <rect x="9" y="11" width="3" height="3" fill="#00ff9f" />
  <rect x="14" y="11" width="3" height="3" fill="#00ff9f" />
</svg>
