export const OPEN_LABEL_PICKER_EVENT = "middleman-open-label-picker";

export type LabelPickerItemType = "pull" | "issue";

export interface OpenLabelPickerDetail {
  itemType: LabelPickerItemType;
  provider: string;
  platformHost?: string | undefined;
  owner: string;
  name: string;
  repoPath: string;
  number: number;
}

export function openLabelPickerFor(detail: OpenLabelPickerDetail): void {
  window.dispatchEvent(new CustomEvent(OPEN_LABEL_PICKER_EVENT, { detail }));
}

export function labelPickerCommandMatches(expected: OpenLabelPickerDetail, actual: OpenLabelPickerDetail): boolean {
  return (
    expected.itemType === actual.itemType &&
    expected.provider === actual.provider &&
    (expected.platformHost ?? "") === (actual.platformHost ?? "") &&
    expected.owner === actual.owner &&
    expected.name === actual.name &&
    expected.repoPath === actual.repoPath &&
    expected.number === actual.number
  );
}
