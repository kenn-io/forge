import { expect, it } from "vite-plus/test";
import { projectMutationKey } from "./project-mutation-workflow.js";

it("distinguishes the self host from a fleet host named local", () => {
  expect(projectMutationKey("register", undefined, ["/srv/repo"])).not.toBe(
    projectMutationKey("register", "local", ["/srv/repo"]),
  );
});
