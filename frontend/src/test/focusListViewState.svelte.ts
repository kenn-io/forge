import type { PullRequest } from "../lib/api/types.js";
import type { PullAttributeFilter } from "../lib/utils/pull-attributes.js";
import type { TriFilter } from "../lib/utils/tri-filter.js";

let pullSearch = $state<string | undefined>(undefined);
let issueSearch = $state<string | undefined>(undefined);
let pullInvolvesMe = $state<TriFilter>(undefined);
let issueInvolvesMe = $state<TriFilter>(undefined);
let pullUnassigned = $state<TriFilter>(undefined);
let issueUnassigned = $state<TriFilter>(undefined);
let issueReferencedByPR = $state<TriFilter>(undefined);
let pullAttributeFilters = $state<PullAttributeFilter[]>([]);
let pullRows = $state<PullRequest[]>([]);

export function getPullAttributeFilters(): PullAttributeFilter[] {
  return pullAttributeFilters;
}

export function setPullAttributeFilters(value: PullAttributeFilter[]): void {
  pullAttributeFilters = value;
}

export function getPullRows(): PullRequest[] {
  return pullRows;
}

export function setPullRows(value: PullRequest[]): void {
  pullRows = value;
}

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

export function getPullInvolvesMe(): TriFilter {
  return pullInvolvesMe;
}

export function getIssueInvolvesMe(): TriFilter {
  return issueInvolvesMe;
}

export function setPullInvolvesMe(value: TriFilter): void {
  pullInvolvesMe = value;
}

export function setIssueInvolvesMe(value: TriFilter): void {
  issueInvolvesMe = value;
}

export function getPullUnassigned(): TriFilter {
  return pullUnassigned;
}

export function getIssueUnassigned(): TriFilter {
  return issueUnassigned;
}

export function setPullUnassigned(value: TriFilter): void {
  pullUnassigned = value;
}

export function setIssueUnassigned(value: TriFilter): void {
  issueUnassigned = value;
}

export function getIssueReferencedByPR(): TriFilter {
  return issueReferencedByPR;
}

export function setIssueReferencedByPR(value: TriFilter): void {
  issueReferencedByPR = value;
}

export function resetFocusListViewState(): void {
  pullSearch = undefined;
  issueSearch = undefined;
  pullInvolvesMe = undefined;
  issueInvolvesMe = undefined;
  pullUnassigned = undefined;
  issueUnassigned = undefined;
  issueReferencedByPR = undefined;
  pullAttributeFilters = [];
  pullRows = [];
}
