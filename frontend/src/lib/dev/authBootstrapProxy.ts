import type { ProxyOptions } from "vite";

export function authBootstrapProxy(apiUrl: string): ProxyOptions {
  const backend = new URL(apiUrl);
  const basePath = backend.pathname.replace(/\/$/, "");
  return {
    target: apiUrl,
    changeOrigin: true,
    configure(proxy) {
      proxy.on("proxyRes", (response) => {
        const location = response.headers.location;
        if (!location || !basePath) return;
        const redirect = new URL(location, backend);
        if (redirect.origin !== backend.origin) return;
        if (redirect.pathname === basePath || redirect.pathname.startsWith(`${basePath}/`)) {
          redirect.pathname = redirect.pathname.slice(basePath.length) || "/";
          response.headers.location = redirect.pathname + redirect.search + redirect.hash;
        }
      });
    },
  };
}
