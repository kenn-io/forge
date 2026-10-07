import { launchURL, serverURL, type Connection } from "./connection.ts";

// JSON is embedded in script text, so a pasted value must not end the script.
const scriptValue = (value: string) => JSON.stringify(value).replaceAll("<", "\\u003c");

export function embeddedPage(bundle: string, connection: Connection, tablet: boolean, target?: string) {
  const server = serverURL(connection.server);
  const baseUrl = target ?? launchURL(connection, tablet);
  const basePath = new URL(server).pathname.replace(/\/$/, "") + "/";
  const bootstrapURL = new URL(server + "/api/v1/settings");
  if (connection.token.trim()) bootstrapURL.searchParams.set("auth_token", connection.token.trim());
  return {
    baseUrl,
    html: `<!doctype html><html><head><meta name="viewport" content="width=device-width, initial-scale=1"><link rel="icon" href="data:,"></head><body><script>
(async () => {
try {
  window.__BASE_PATH__ = ${scriptValue(basePath)};
  history.replaceState(null, "", ${scriptValue(baseUrl)});
  const response = await fetch(${scriptValue(bootstrapURL.href)}, { credentials: "include" });
  if (!response.ok) throw new Error(response.status === 401 || response.status === 403
    ? "The server refused this connection. Check your auth token and the server's access settings."
    : "The server returned HTTP " + response.status + ". Try again.");
  const compressed = Uint8Array.from(atob(${scriptValue(bundle)}), character => character.charCodeAt(0));
  const stream = new Blob([compressed]).stream().pipeThrough(new DecompressionStream("gzip"));
  const html = await new Response(stream).text();
  document.open();
  document.write(html);
  document.close();
} catch (error) {
  window.ReactNativeWebView.postMessage(JSON.stringify({ type: "connection-error", message: error.message || "Could not connect to Forge. Check your network and server address." }));
}
})();
</script></body></html>`,
  };
}
