<script lang="ts">
  import type { Snippet } from "svelte";
  import { setMobileNavMenuContext, type MobileNavMenuContext } from "./mobile-nav-menu.js";

  interface Props {
    nav: MobileNavMenuContext;
    children: Snippet;
  }

  let { nav, children }: Props = $props();

  // The context object is fixed for the life of the phone shell; reactive
  // values such as the current page are read through its getters.
  // svelte-ignore state_referenced_locally
  setMobileNavMenuContext(nav);
</script>

{@render children()}
