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
  const route = connection.desktop && tablet ? "/terminal" : "/m/workspaces";
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

export function isServerURL(target: string, server: string): boolean {
  try {
    const url = new URL(target);
    const base = new URL(server);
    return url.origin === base.origin;
  } catch {
    return false;
  }
}
