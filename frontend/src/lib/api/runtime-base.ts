type RuntimeWindow = Window & { __BASE_PATH__?: string };

function runtimeBasePath(): string {
  return typeof window === "undefined" ? "/" : ((window as RuntimeWindow).__BASE_PATH__ ?? "/");
}

function basePathPrefix(basePath: string): string {
  let end = basePath.length;
  while (end > 0 && basePath[end - 1] === "/") end -= 1;
  return basePath.slice(0, end);
}

export function configuredAPIBasePath(basePath = runtimeBasePath()): string {
  return `${basePathPrefix(basePath)}/api/v1`;
}

/** The server's reverse proxy to the local roborev daemon. */
export function configuredRoborevBasePath(basePath = runtimeBasePath()): string {
  return `${basePathPrefix(basePath)}/api/roborev`;
}

export function configuredAPIBaseURL(basePath = runtimeBasePath()): string {
  const origin = typeof window === "undefined" ? "http://localhost" : window.location.origin;
  return new URL(configuredAPIBasePath(basePath), origin).toString();
}

export function configuredAPIPath(path: string, basePath = runtimeBasePath()): string {
  return `${configuredAPIBasePath(basePath)}${path.startsWith("/") ? path : `/${path}`}`;
}
