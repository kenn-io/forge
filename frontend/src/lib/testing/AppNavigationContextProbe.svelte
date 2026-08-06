<script lang="ts">
  import { getNavigate } from "../context.js";

  const navigate = getNavigate();
</script>

<button
  type="button"
  onclick={() =>
    navigate(
      {
        path: "/issues",
        route: { page: "issues" },
      },
      { replace: true },
    )}
>
  Replace navigation probe
</button>
