import type { KataRecurrence } from "../../api/kata/taskTypes.js";

export class KataRecurrenceConflictError extends Error {
  readonly recurrence: KataRecurrence;
  readonly etag: string;

  constructor(message: string, recurrence: KataRecurrence, etag: string) {
    super(message);
    this.name = "KataRecurrenceConflictError";
    this.recurrence = recurrence;
    this.etag = etag;
  }
}
