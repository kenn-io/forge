import { describe, expect, it } from "vite-plus/test";

import type { Folder } from "../../api/docs/types";
import { effectiveDocsFolderDaemon } from "./folderDaemon";

const folders: Folder[] = [
  { id: "notes", name: "Notes", path: "/notes", daemon: "work" },
  { id: "archive", name: "Archive", path: "/archive", daemon: "gone" },
  { id: "inbox", name: "Inbox", path: "/inbox" },
];

describe("effectiveDocsFolderDaemon", () => {
  it("uses a folder daemon binding only when the live roster still contains it", () => {
    expect(effectiveDocsFolderDaemon(folders, "notes", ["home", "work"])).toBe("work");
    expect(effectiveDocsFolderDaemon(folders, "archive", ["home", "work"])).toBeUndefined();
    expect(effectiveDocsFolderDaemon(folders, "inbox", ["home", "work"])).toBeUndefined();
  });
});
