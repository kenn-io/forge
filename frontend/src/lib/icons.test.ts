import { describe, expect, it } from "vite-plus/test";

import * as icons from "./icons.ts";

const approvedIconNames = [
  "AlertIcon",
  "ChevronDownIcon",
  "ExternalLinkIcon",
  "FolderTreeIcon",
  "MergeConflictIcon",
  "MonitorIcon",
  "MoonIcon",
  "PaperclipIcon",
  "RefreshIcon",
  "SearchIcon",
  "SettingsIcon",
  "SidebarToggleIcon",
  "SpinnerIcon",
  "SunIcon",
  "SyncIcon",
] as const;

describe("icons barrel", () => {
  it("exports the approved app icon set", () => {
    expect(Object.keys(icons).sort()).toEqual([...approvedIconNames].sort());
  });
});
