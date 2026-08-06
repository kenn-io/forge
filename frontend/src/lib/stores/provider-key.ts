import { canonicalProvider, resolvedPlatformHost } from "../api/provider-routes.js";

export interface ProviderItemKey {
  readonly provider: string;
  readonly platformHost: string;
  readonly owner: string;
  readonly name: string;
  readonly number: number;
}

export function providerItemKey(ref: ProviderItemKey): string {
  const provider = canonicalProvider(ref.provider);
  const platformHost = resolvedPlatformHost(provider, ref.platformHost);
  return [provider, platformHost, ref.owner, ref.name, String(ref.number)].map(encodeURIComponent).join("\u0000");
}

export function providerMutationKey(itemType: "pull" | "issue", ref: ProviderItemKey, family: string): string {
  return `${itemType}\u0000${providerItemKey(ref)}\u0000${family}`;
}
