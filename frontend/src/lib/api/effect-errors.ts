import { Data, Schema } from "effect";
import type { ProblemBody } from "./problems.js";

export class TransientTransportError extends Schema.TaggedErrorClass<TransientTransportError>()(
  "TransientTransportError",
  {
    operation: Schema.String,
    cause: Schema.Defect(),
  },
) {}

export class InvalidExternalPayload extends Schema.TaggedErrorClass<InvalidExternalPayload>()(
  "InvalidExternalPayload",
  {
    operation: Schema.String,
    cause: Schema.Defect(),
  },
) {}

export class BrowserStreamError extends Schema.TaggedErrorClass<BrowserStreamError>()("BrowserStreamError", {
  operation: Schema.String,
  cause: Schema.Defect(),
}) {}

export class ApiProblemError extends Data.TaggedError("ApiProblemError")<{
  readonly operation: string;
  readonly problem: ProblemBody;
}> {}

export class UnsupportedCapabilityError extends Data.TaggedError("UnsupportedCapabilityError")<{
  readonly operation: string;
  readonly capability: string;
  readonly problem: ProblemBody;
}> {}
