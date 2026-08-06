<script lang="ts">
  import { Chip,
    type ChipSize,
    type ChipTone,
  } from "@kenn-io/kit-ui";

  interface Props {
    state: string;
    size?: ChipSize;
    title?: string;
    class?: string;
  }

  const {
    state,
    size = "sm",
    title = undefined,
    class: className = "",
  }: Props = $props();

  const normalized = $derived(state.toLowerCase());
  const label = $derived(
    normalized === "pr" ? "PR" : normalized[0]?.toUpperCase() + normalized.slice(1),
  );
  const tone: ChipTone = $derived.by(() => {
    switch (normalized) {
      case "open":
        return "success";
      case "draft":
        return "warning";
      case "merged":
        return "merged";
      case "closed":
        return "danger";
      default:
        return "muted";
    }
  });
  const chipClass = $derived(
    [
      "state-badge",
      `state-${normalized}`,
      "chip--state",
      `chip--state-${normalized}`,
      className,
    ]
      .filter(Boolean)
      .join(" "),
  );
</script>

<Chip {size} {tone} {title} class={chipClass}>
  {label}
</Chip>
