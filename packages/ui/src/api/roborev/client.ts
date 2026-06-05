import createClient from "openapi-fetch";
import type { paths } from "./generated/schema.js";
import { csrfFetch, type FetchFn } from "../csrf.js";

export type RoborevClient = ReturnType<typeof createClient<paths>>;

export function createRoborevClient(
  baseUrl: string,
  fetchFn?: FetchFn,
): RoborevClient {
  const inner = fetchFn ?? globalThis.fetch.bind(globalThis);
  return createClient<paths>({
    baseUrl,
    fetch: csrfFetch(inner),
  });
}
