<script lang="ts">
  interface Props {
    reviewDraftMutation?: boolean;
    reviewUnavailableReason?: string | undefined;
    keyboardActive?: boolean;
    pageKeyboardActive?: boolean;
  }

  const {
    reviewDraftMutation = false,
    reviewUnavailableReason = undefined,
    keyboardActive = true,
    pageKeyboardActive = keyboardActive,
  }: Props = $props();
</script>

<div data-testid="review-draft-mutation">{String(reviewDraftMutation)}</div>
<div data-testid="review-unavailable-reason">{reviewUnavailableReason ?? ""}</div>
<div data-testid="keyboard-active">{String(keyboardActive)}</div>
<div data-testid="page-keyboard-active">{String(pageKeyboardActive)}</div>
