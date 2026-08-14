let pullSearch = $state<string | undefined>(undefined);
let issueSearch = $state<string | undefined>(undefined);
let pullInvolvesMe = $state(false);
let issueInvolvesMe = $state(false);

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

export function getPullInvolvesMe(): boolean {
  return pullInvolvesMe;
}

export function getIssueInvolvesMe(): boolean {
  return issueInvolvesMe;
}

export function setPullInvolvesMe(value: boolean): void {
  pullInvolvesMe = value;
}

export function setIssueInvolvesMe(value: boolean): void {
  issueInvolvesMe = value;
}

export function resetFocusListViewState(): void {
  pullSearch = undefined;
  issueSearch = undefined;
  pullInvolvesMe = false;
  issueInvolvesMe = false;
}
