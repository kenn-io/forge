import type { IncomingMessage, ServerResponse } from "node:http";
import type { Plugin } from "vite";

type NextFunction = (err?: unknown) => void;

const healthResponse = JSON.stringify({ status: "ok" });
const healthPaths = new Set(["/healthz", "/livez"]);

function handleHealthcheck(req: IncomingMessage, res: ServerResponse, next: NextFunction) {
  const pathname = new URL(req.url ?? "/", "http://127.0.0.1").pathname;
  if (!healthPaths.has(pathname)) {
    next();
    return;
  }

  res.statusCode = 200;
  res.setHeader("Content-Type", "application/json; charset=utf-8");
  res.end(healthResponse);
}

export function healthcheckPlugin(): Plugin {
  return {
    name: "middleman-healthcheck",
    configurePreviewServer(server) {
      server.middlewares.use(handleHealthcheck);
    },
    configureServer(server) {
      server.middlewares.use(handleHealthcheck);
    },
  };
}
