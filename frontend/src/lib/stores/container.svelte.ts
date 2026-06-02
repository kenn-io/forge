type ContainerSize = "narrow" | "medium" | "wide";

let currentSize = $state<ContainerSize>("wide");
let observer: ResizeObserver | null = null;
let debounceTimer: ReturnType<typeof setTimeout> | null = null;

function classify(width: number): ContainerSize {
  if (width < 500) return "narrow";
  if (width < 1100) return "medium";
  return "wide";
}

export function initContainerObserver(el: HTMLElement): () => void {
  function apply(size: ContainerSize): void {
    currentSize = size;
    el.classList.remove("container-narrow", "container-medium");
    if (size === "narrow") {
      el.classList.add("container-narrow");
    } else if (size === "medium") {
      el.classList.add("container-medium");
    }
  }

  // Initial measurement
  apply(classify(el.clientWidth));

  observer = new ResizeObserver((entries) => {
    if (debounceTimer) clearTimeout(debounceTimer);
    debounceTimer = setTimeout(() => {
      const entry = entries[0];
      if (entry) {
        apply(classify(entry.contentRect.width));
      }
    }, 100);
  });
  observer.observe(el);

  return () => {
    observer?.disconnect();
    observer = null;
    if (debounceTimer) clearTimeout(debounceTimer);
  };
}

export function getContainerSize(): ContainerSize {
  return currentSize;
}

export function isNarrow(): boolean {
  return currentSize === "narrow";
}
