import { providerDisplayLabel } from "@kenn-forge/ui/api/provider-labels";

export interface RepoImportProvider {
  id: string;
  label: string;
  defaultHost: string;
  allowNestedOwner: boolean;
  ownerPatternPlaceholder: string;
}

export const defaultRepoImportProvider: RepoImportProvider = {
  id: "github",
  label: providerDisplayLabel("github"),
  defaultHost: "github.com",
  allowNestedOwner: false,
  ownerPatternPlaceholder: "owner/pattern",
};

export const repoImportProviders: RepoImportProvider[] = [
  defaultRepoImportProvider,
  {
    id: "gitlab",
    label: providerDisplayLabel("gitlab"),
    defaultHost: "gitlab.com",
    allowNestedOwner: true,
    ownerPatternPlaceholder: "group/subgroup/pattern",
  },
  {
    id: "forgejo",
    label: providerDisplayLabel("forgejo"),
    defaultHost: "codeberg.org",
    allowNestedOwner: false,
    ownerPatternPlaceholder: "owner/pattern",
  },
  {
    id: "gitea",
    label: providerDisplayLabel("gitea"),
    defaultHost: "gitea.com",
    allowNestedOwner: false,
    ownerPatternPlaceholder: "owner/pattern",
  },
];

export function repoImportProvider(id: string): RepoImportProvider {
  return repoImportProviders.find((provider) => provider.id === id) ?? defaultRepoImportProvider;
}
