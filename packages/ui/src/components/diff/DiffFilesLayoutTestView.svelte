<script lang="ts">
  interface Props {
    reviewDraftMutation?: boolean;
    reviewUnavailableReason?: string | undefined;
  }

  const {
    reviewDraftMutation = false,
    reviewUnavailableReason = undefined,
  }: Props = $props();
</script>

<div data-testid="review-draft-mutation">{String(reviewDraftMutation)}</div>
<div data-testid="review-unavailable-reason">{reviewUnavailableReason ?? ""}</div>
