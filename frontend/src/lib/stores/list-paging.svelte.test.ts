import { expect, it } from "vite-plus/test";
import { createListPaging } from "./list-paging.svelte.js";

it("clears an older page failure after a successful refresh", () => {
  const paging = createListPaging();
  const query = { state: "closed" };
  paging.apply(paging.withLimit(query));
  paging.loadMore(true);
  paging.rollback(paging.withLimit(query), undefined, { detail: "Unavailable", retryable: true });
  expect(paging.problem).not.toBeNull();
  paging.apply(paging.withLimit(query));
  expect(paging.problem).toBeNull();
});
