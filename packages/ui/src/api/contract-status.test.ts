import { describe, expect, it } from "vite-plus/test";

import { classifyOpenAPIResponse, classifyRouteCapability } from "./contract-status";

const openAPIPath = "/api/v1/openapi.json";

describe("contract status", () => {
  it("classifies an exposed OpenAPI document", () => {
    expect(classifyOpenAPIResponse(200, { openapi: "3.1.0", paths: {} }, openAPIPath)).toEqual({
      available: true,
      reason: "available",
      path: openAPIPath,
    });
  });

  it("classifies a disabled OpenAPI route without failing diagnostics", () => {
    expect(classifyOpenAPIResponse(404, { status: 404 }, openAPIPath)).toEqual({
      available: false,
      reason: "not_exposed",
      path: openAPIPath,
    });
  });

  it("classifies a supported route", () => {
    expect(classifyRouteCapability({ route: "/api/v1/kata/daemons", status: 200 })).toEqual({
      route: "/api/v1/kata/daemons",
      available: true,
      reason: "available",
    });
  });

  it("classifies a missing route as a capability gap", () => {
    expect(classifyRouteCapability({ route: "/api/v1/kata/proxy/api/v1/issues?status=open", status: 404 })).toEqual({
      route: "/api/v1/kata/proxy/api/v1/issues?status=open",
      available: false,
      reason: "not_exposed",
    });
  });
});
