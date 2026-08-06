export interface ContractStatus {
  available: boolean;
  reason: "available" | "not_exposed" | "invalid";
  path: string;
}

export interface RouteCapability {
  route: string;
  available: boolean;
  reason: "available" | "not_exposed" | "auth_required" | "server_error" | "invalid";
}

function hasOpenAPIShape(body: unknown): boolean {
  return typeof body === "object" && body !== null && "openapi" in body && "paths" in body;
}

export function classifyOpenAPIResponse(status: number, body: unknown, path: string): ContractStatus {
  if (status === 200 && hasOpenAPIShape(body)) {
    return { available: true, reason: "available", path };
  }
  if (status === 404 || status === 405) {
    return { available: false, reason: "not_exposed", path };
  }
  return { available: false, reason: "invalid", path };
}

export function classifyRouteCapability(input: { route: string; status: number }): RouteCapability {
  if (input.status >= 200 && input.status < 300) {
    return { route: input.route, available: true, reason: "available" };
  }
  if (input.status === 401 || input.status === 403) {
    return { route: input.route, available: false, reason: "auth_required" };
  }
  if (input.status === 404 || input.status === 405) {
    return { route: input.route, available: false, reason: "not_exposed" };
  }
  if (input.status >= 500) {
    return { route: input.route, available: false, reason: "server_error" };
  }
  return { route: input.route, available: false, reason: "invalid" };
}
