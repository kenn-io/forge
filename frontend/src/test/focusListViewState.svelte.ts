let pullSearch = $state<string | undefined>(undefined);
let issueSearch = $state<string | undefined>(undefined);

export function getPullSearch(): string | undefined {
  return pullSearch;
}

export function getIssueSearch(): string | undefined {
  return issueSearch;
}

export function setPullSearch(value: string | undefined): void {
  pullSearch = value;
}

export function setIssueSearch(value: string | undefined): void {
  issueSearch = value;
}

export function resetFocusListViewState(): void {
  pullSearch = undefined;
  issueSearch = undefined;
}
