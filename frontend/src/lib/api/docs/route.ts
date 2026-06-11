export type DocsRoute = {
  mode: "docs";
  folder: string | null;
  doc: string | null;
};

export function parseDocsRoute(search: string): DocsRoute {
  const params = new URLSearchParams(search.startsWith("?") ? search.slice(1) : search);
  return {
    mode: "docs",
    folder: emptyToNull(params.get("folder")),
    doc: emptyToNull(params.get("doc")),
  };
}

export function docsSearch(route: DocsRoute): string {
  const params = new URLSearchParams();
  if (route.folder) params.set("folder", route.folder);
  if (route.doc) params.set("doc", route.doc);
  const qs = params.toString();
  return qs ? `?${qs}` : "";
}

export function docsHref(route: DocsRoute): string {
  const qs = docsSearch(route);
  return qs ? `/docs${qs}` : "/docs";
}

function emptyToNull(value: string | null): string | null {
  return value && value.length > 0 ? value : null;
}

export const defaultDocsRoute: DocsRoute = { mode: "docs", folder: null, doc: null };
