import type { ProxyOptions } from "vite";

const maxBodyBytes = 64 * 1024;

// Vite stdout is retained by process-compose even after the browser closes.
export function apiFailureLogging(
  enabled = process.env.KENN_FORGE_API_ERROR_DEBUG === "1",
): NonNullable<ProxyOptions["configure"]> {
  return (proxy) => {
    if (!enabled) return;
    proxy.on("proxyRes", (response, request) => {
      if ((response.statusCode ?? 0) < 400) return;
      const chunks: Buffer[] = [];
      let bytes = 0;
      let captured = 0;
      response.on("data", (chunk: Buffer) => {
        bytes += chunk.length;
        const part = chunk.subarray(0, maxBodyBytes - captured);
        if (part.length > 0) chunks.push(Buffer.from(part));
        captured += part.length;
      });
      response.once("close", () => {
        const contentEncoding = response.headers["content-encoding"];
        const bodyEncoding = contentEncoding && contentEncoding !== "identity" ? "base64" : "utf8";
        console.error(
          JSON.stringify({
            event: "dev.api_failure",
            time: new Date().toISOString(),
            method: request.method,
            path: request.url?.split("?")[0],
            status: response.statusCode,
            contentType: response.headers["content-type"],
            contentEncoding,
            traceparent: request.headers.traceparent,
            complete: response.complete,
            bodyEncoding,
            body: Buffer.concat(chunks).toString(bodyEncoding),
            bodyBytes: bytes,
            truncated: bytes > captured,
          }),
        );
      });
    });
    proxy.on("error", (error, request) => {
      console.error(
        JSON.stringify({
          event: "dev.api_proxy_error",
          time: new Date().toISOString(),
          method: request.method,
          path: request.url?.split("?")[0],
          traceparent: request.headers.traceparent,
          error: error.message,
        }),
      );
    });
  };
}
