import { Effect } from "effect";
import { executeRoborevRequest, type RoborevClient } from "../../api/roborev/client.js";
import * as roborevAPI from "../../api/roborev/generated/client.js";
import { RoborevResponseError } from "./roborev-workflow.js";

/** The repository root roborev tracks for a checkout path, or undefined when roborev does not track it. */
export const resolveRoborevRepoRoot = Effect.fn("Roborev.resolveRepoRoot")(function* (
  client: RoborevClient,
  path: string,
) {
  const result = yield* executeRoborevRequest("resolve workspace Roborev repository", (signal) =>
    roborevAPI.resolveRepo({ path }, { signal }, client),
  );
  if (result.status !== 200) {
    return yield* Effect.fail(
      RoborevResponseError.make({
        operation: "resolve workspace Roborev repository",
        message: "Failed to resolve repository",
        cause: result.data,
      }),
    );
  }
  return result.data.tracked ? result.data.repo?.root_path : undefined;
});
