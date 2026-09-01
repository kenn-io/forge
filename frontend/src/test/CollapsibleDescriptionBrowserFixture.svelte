<script lang="ts">
  import CollapsibleDescription from "../lib/components/detail/CollapsibleDescription.svelte";
</script>

<CollapsibleDescription
  source={"x".repeat(1_501)}
  copied={false}
  oncopy={() => {}}
>
  {#snippet headerActions()}
    <button type="button" class="fixture-edit">Edit</button>
  {/snippet}
  <div style="height: 640px">Long rendered description</div>
</CollapsibleDescription>
