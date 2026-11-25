import { launchURL, serverURL, type Connection } from "./connection.ts";

// The Android app answers requests under this path on the server's origin
// from the web UI files packaged in the APK, so the page and its scripts,
// styles, and fonts stay same-origin with the server's API.
export const appAssetsPath = "/__forge_app__/";

// JSON is embedded in script text, so a pasted value must not end the script.
const scriptValue = (value: string) => JSON.stringify(value).replaceAll("<", "\\u003c");

// page, a path with an optional fragment such as a kenn:// link names, opens
// instead of the launch route.
export function embeddedPage(connection: Connection, tablet: boolean, page?: string) {
  const server = serverURL(connection.server);
  const baseUrl = page ? server + page : launchURL(connection, tablet);
  const basePath = new URL(server).pathname.replace(/\/$/, "") + "/";
  const bootstrapURL = new URL(server + "/api/v1/settings");
  if (connection.token.trim()) bootstrapURL.searchParams.set("auth_token", connection.token.trim());
  return {
    baseUrl,
    html: `<!doctype html><html><head><meta name="viewport" content="width=device-width, initial-scale=1"><link rel="icon" href="data:,"><style>html{color-scheme:light dark}body{margin:0;min-height:100vh;display:grid;place-items:center;background:Canvas;color:CanvasText;font:1rem system-ui}</style></head><body><p role="status">Connecting to Forge…</p><script>
(async () => {
try {
  window.__BASE_PATH__ = ${scriptValue(basePath)};
  window.__KENN_EMBEDDED_KENN_LINKS__ = true;
  history.replaceState(null, "", ${scriptValue(baseUrl)});
  const response = await fetch(${scriptValue(bootstrapURL.href)}, { credentials: "include" });
  if (!response.ok) throw new Error(response.status === 401 || response.status === 403
    ? "The server refused this connection. Check your auth token and the server's access settings."
    : "The server returned HTTP " + response.status + ". Try again.");
  const app = await fetch(${scriptValue(new URL(server).origin + appAssetsPath + "index.html")});
  if (!app.ok) throw new Error("This app is missing its Forge web UI. Reinstall the app.");
  const html = await app.text();
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
