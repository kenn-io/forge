export interface RepoImportProvider {
  id: string;
  label: string;
  defaultHost: string;
  allowNestedOwner: boolean;
  ownerPatternPlaceholder: string;
}

export const defaultRepoImportProvider: RepoImportProvider = {
  id: "github",
  label: "GitHub",
  defaultHost: "github.com",
  allowNestedOwner: false,
  ownerPatternPlaceholder: "owner/pattern",
};

export const repoImportProviders: RepoImportProvider[] = [
  defaultRepoImportProvider,
  {
    id: "gitlab",
    label: "GitLab",
    defaultHost: "gitlab.com",
    allowNestedOwner: true,
    ownerPatternPlaceholder: "group/subgroup/pattern",
  },
  {
    id: "forgejo",
    label: "Forgejo",
    defaultHost: "codeberg.org",
    allowNestedOwner: false,
    ownerPatternPlaceholder: "owner/pattern",
  },
  {
    id: "gitea",
    label: "Gitea",
    defaultHost: "gitea.com",
    allowNestedOwner: false,
    ownerPatternPlaceholder: "owner/pattern",
  },
];

export function repoImportProvider(id: string): RepoImportProvider {
  return repoImportProviders.find((provider) => provider.id === id) ?? defaultRepoImportProvider;
}
