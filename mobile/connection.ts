export interface Connection {
  server: string;
  token: string;
  desktop: boolean;
}

export function serverURL(input: string): string {
  let url: URL;
  try {
    url = new URL(input.trim());
  } catch {
    throw new Error("Enter the full server URL, including http:// or https://.");
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") {
    throw new Error("Use an http:// or https:// server URL.");
  }
  if (url.username || url.password || url.search || url.hash) {
    throw new Error(
      "Enter the server URL without credentials, query parameters, or a fragment. Use the token field to sign in.",
    );
  }
  return url.toString().replace(/\/+$/, "");
}

export function launchURL(connection: Connection, tablet: boolean): string {
  const route = connection.desktop && tablet ? "/workspaces" : "/m/workspaces";
  const url = new URL(serverURL(connection.server) + route);
  if (connection.desktop && tablet) url.searchParams.set("desktop", "1");
  return url.toString();
}

export function readConnection(value: string): Connection {
  const data: unknown = JSON.parse(value);
  if (
    typeof data !== "object" ||
    data === null ||
    !("server" in data) ||
    typeof data.server !== "string" ||
    !("token" in data) ||
    typeof data.token !== "string" ||
    !("desktop" in data) ||
    typeof data.desktop !== "boolean"
  )
    throw new Error("Saved connection could not be read. Enter your server again.");
  return { server: serverURL(data.server), token: data.token, desktop: data.desktop };
}

// Top-frame navigations stay in the bundled SPA only under the server's base path;
// other paths on the same host belong to other apps.
export function isForgePageURL(target: string, server: string): boolean {
  try {
    const url = new URL(target);
    const base = new URL(server);
    const path = base.pathname.replace(/\/+$/, "");
    return url.origin === base.origin && (!path || url.pathname === path || url.pathname.startsWith(path + "/"));
  } catch {
    return false;
  }
}

export function isServerURL(target: string, server: string): boolean {
  try {
    const url = new URL(target);
    const base = new URL(server);
    return url.origin === base.origin;
  } catch {
    return false;
  }
}

// A kenn:// link (kenn://kenn-forge[/@<node>]/<path>[?<query>][#<anchor>])
// names a Forge page but no server; the app opens it on the connected server.
// The routes, their query keys and anchors, and the parsing rules match the
// allowlist of the host app that writes these links. A page that acts on
// load is refused, and a query key a route does not list rejects the link,
// so a link cannot add parameters such as auth_token.
// React Native's URL parses only http(s) hosts and paths, hence the patterns.
const kennForgeLink = /^kenn:\/\/kenn-forge(?=[/?#]|$)([^?#]*)(\?[^#]*)?(#.*)?$/;
const unsafeCharacter = /[\s\p{Cc}\\]/u;
const dotSegment = /(?:^|\/)(?:\.|%2e){1,2}(?:\/|$)/i;

interface LinkRoute {
  path: RegExp;
  query?: string[];
  anchor?: boolean;
  refused?: true;
}

// An optional platform host, then provider, owner, repository, and number.
const forgeItem = (kind: "pulls" | "issues") => `(?:host/[^/]+/)?${kind}/[^/]+/[^/]+/[^/]+/\\d+`;

const forgeLinkRoutes: LinkRoute[] = [
  { path: /^(?:actions|settings|project-intake|workspaces(?:\/.*)?|terminal\/.+|m(?:\/.*)?)$/, refused: true },
  { path: /^(?:|pulls|issues|repos)$/ },
  { path: new RegExp(`^(?:focus/)?${forgeItem("pulls")}(?:/files)?$`), anchor: true },
  { path: new RegExp(`^(?:focus/)?${forgeItem("issues")}$`), anchor: true },
  { path: /^focus\/(?:mrs|issues)$/, query: ["repo"] },
  {
    path: /^repo\/browser$/,
    query: ["provider", "platform_host", "repo_path", "ref_type", "ref_name", "ref_sha", "path", "mode"],
    anchor: true,
  },
  { path: /^worklists\/import$/, anchor: true },
];

const notRecognized = "This version of Forge cannot open this link.";

function queryKeys(search: string): string[] | null {
  try {
    return search
      .split("&")
      .filter(Boolean)
      .map((pair) => decodeURIComponent(pair.split("=", 1)[0]!.replaceAll("+", " ")));
  } catch {
    return null;
  }
}

export function kennLinkPage(link: string): { page: string } | { error: string } {
  const match = /^kenn:/i.test(link) ? kennForgeLink.exec("kenn:" + link.slice(5)) : null;
  if (!match || unsafeCharacter.test(link) || dotSegment.test(match[1]!)) return { error: notRecognized };
  const path = match[1]!.replace(/^\/+/, "").replace(/\/+$/, "");
  if (path.startsWith("@")) {
    return { error: "This link opens a page on another Forge computer, which this app cannot reach." };
  }
  const search = match[2] === "?" ? "" : (match[2] ?? "");
  const hash = match[3] === "#" ? "" : (match[3] ?? "");
  const route = forgeLinkRoutes.find((candidate) => candidate.path.test(path));
  if (!route) return { error: notRecognized };
  if (route.refused) return { error: "Forge cannot open this page from a link. Open it in Forge instead." };
  const keys = queryKeys(search.slice(1));
  if (!keys || keys.some((key) => !route.query?.includes(key)) || (hash && !route.anchor)) {
    return { error: notRecognized };
  }
  return { page: `/${path}${search}${hash}` };
}
