interface ListQuery {
  state?: string;
  limit?: number;
}

const PAGE_SIZE = 200;

export function createListPaging() {
  let page = $state.raw({ key: "", want: PAGE_SIZE, got: PAGE_SIZE });

  function withLimit<T extends ListQuery>(query: T): T {
    if (query.limit === undefined) {
      const key = JSON.stringify(query);
      if (key !== page.key) page = { key, want: PAGE_SIZE, got: PAGE_SIZE };
      if (query.state !== "open") return { ...query, limit: page.want };
    }
    return query;
  }

  function isCurrentQuery(query: ListQuery, params?: ListQuery): boolean {
    const { limit: _limit, ...withoutLimit } = query;
    return params?.limit === undefined && JSON.stringify(withoutLimit) === page.key;
  }

  function apply({ limit: want, ...query }: ListQuery, params?: ListQuery): void {
    if (want !== undefined && isCurrentQuery(query, params)) page = { ...page, want, got: want };
  }

  function isLoadingMore(): boolean {
    return page.want > page.got;
  }

  function getLimit(): number {
    return page.want;
  }

  function rollback(query: ListQuery, params?: ListQuery): boolean {
    if (query.limit !== page.want || !isCurrentQuery(query, params)) return false;
    const wasLoadingMore = isLoadingMore();
    page = { ...page, want: page.got };
    return wasLoadingMore;
  }

  function loadMore(capped: boolean): boolean {
    if (!capped || isLoadingMore()) return false;
    page = { ...page, want: page.got + PAGE_SIZE };
    return true;
  }

  return { withLimit, apply, getLimit, isLoadingMore, rollback, loadMore };
}
