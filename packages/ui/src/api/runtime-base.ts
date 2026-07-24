type RuntimeWindow = Window & { __BASE_PATH__?: string };

function runtimeBasePath(): string {
  return typeof window === "undefined" ? "/" : ((window as RuntimeWindow).__BASE_PATH__ ?? "/");
}

export function configuredAPIBasePath(basePath = runtimeBasePath()): string {
  let end = basePath.length;
  while (end > 0 && basePath[end - 1] === "/") end -= 1;
  const prefix = basePath.slice(0, end);
  return `${prefix}/api/v1`;
}

export function configuredAPIBaseURL(basePath = runtimeBasePath()): string {
  const origin = typeof window === "undefined" ? "http://localhost" : window.location.origin;
  return new URL(configuredAPIBasePath(basePath), origin).toString();
}

export function configuredAPIPath(path: string, basePath = runtimeBasePath()): string {
  return `${configuredAPIBasePath(basePath)}${path.startsWith("/") ? path : `/${path}`}`;
}
