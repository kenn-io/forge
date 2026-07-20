import { canonicalProvider } from "./provider-routes.js";

const displayLabels: Record<string, string> = {
  github: "GitHub",
  gitlab: "GitLab",
  forgejo: "Forgejo",
  gitea: "Gitea",
};

// providerDisplayLabel maps a provider key to its user-facing label.
// Unknown providers fall back to the raw key so UI copy still renders.
export function providerDisplayLabel(provider: string): string {
  return displayLabels[canonicalProvider(provider)] ?? provider;
}
