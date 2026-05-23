import {
  canonicalProvider,
  providerRepoPath,
  providerRouteParams,
} from "@middleman/ui/api/provider-routes";
import { client } from "../api/runtime.js";
import { navigate, buildItemRoute } from "../stores/router.svelte.js";
import { showFlash } from "../stores/flash.svelte.js";

let requestId = 0;
type ItemRefType = "pr" | "issue";

function safeExternalURL(raw: string | undefined): string | null {
  if (!raw) return null;
  try {
    const url = new URL(raw);
    if (url.protocol === "http:" || url.protocol === "https:") {
      return url.href;
    }
  } catch {
    return null;
  }
  return null;
}

function findItemRef(target: EventTarget | null): HTMLAnchorElement | null {
  let el = target as HTMLElement | null;
  while (el) {
    if (el instanceof HTMLAnchorElement && el.classList.contains("item-ref")) {
      return el;
    }
    el = el.parentElement;
  }
  return null;
}

async function resolveAndNavigate(
  provider: string,
  platformHost: string | undefined,
  owner: string,
  name: string,
  repoPath: string,
  number: number,
  itemType: ItemRefType | undefined,
  externalUrl: string | undefined,
  thisRequestId: number,
): Promise<void> {
  try {
    const ref = { provider, platformHost, owner, name, repoPath };
    const itemTypeHint =
      canonicalProvider(provider) === "gitlab" ? itemType : undefined;
    const { data, error, response } = await client.POST(
      providerRepoPath(ref, "/resolve/{number}"),
      {
        params: {
          path: { ...providerRouteParams(ref), number },
          ...(itemTypeHint && { query: { item_type: itemTypeHint } }),
        },
      },
    );

    if (thisRequestId !== requestId) return;

    if (error) {
      if (response.status === 404) {
        showFlash(`Item ${owner}/${name}#${number} not found.`);
      } else {
        showFlash(`Failed to resolve ${owner}/${name}#${number}. Try again later.`);
      }
      return;
    }

    if (!data.repo_tracked) {
      const safeExternalUrl = safeExternalURL(externalUrl);
      if (safeExternalUrl) {
        window.open(safeExternalUrl, "_blank", "noopener,noreferrer");
        return;
      }
      showFlash(
        `${owner}/${name} is not tracked. Add it in Settings to navigate here.`,
      );
      return;
    }

    const path = buildItemRoute({
      itemType: data.item_type === "pr" ? "pr" : "issue",
      provider: ref.provider,
      platformHost: ref.platformHost,
      owner,
      name,
      repoPath,
      number,
    });
    navigate(path);
  } catch {
    if (thisRequestId !== requestId) return;
    showFlash("Failed to resolve item reference. Check your connection.");
  }
}

function handleClick(e: MouseEvent): void {
  if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;

  const anchor = findItemRef(e.target);
  if (!anchor) return;

  const provider = anchor.dataset.provider;
  const platformHost = anchor.dataset.platformHost;
  const owner = anchor.dataset.owner;
  const name = anchor.dataset.name;
  const repoPath = anchor.dataset.repoPath;
  const numberStr = anchor.dataset.number;
  const itemType =
    anchor.dataset.itemType === "pr" || anchor.dataset.itemType === "issue"
      ? anchor.dataset.itemType
      : undefined;
  const externalUrl = anchor.dataset.externalUrl;
  if (!provider || !owner || !name || !repoPath || !numberStr) return;

  e.preventDefault();
  requestId++;
  void resolveAndNavigate(
    provider,
    platformHost,
    owner,
    name,
    repoPath,
    parseInt(numberStr, 10),
    itemType,
    externalUrl,
    requestId,
  );
}

export function initItemRefHandler(): () => void {
  document.addEventListener("click", handleClick);
  return () => document.removeEventListener("click", handleClick);
}
