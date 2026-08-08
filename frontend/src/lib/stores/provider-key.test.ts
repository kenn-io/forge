import { describe, expect, it } from "vite-plus/test";
import { providerItemKey, providerMutationKey } from "./provider-key.js";

describe("providerItemKey", () => {
  it("keeps identical repository items on different hosts independent", () => {
    const common = {
      provider: "gitlab",
      owner: "acme",
      name: "widgets",
      number: 17,
    };

    expect(providerItemKey({ ...common, platformHost: "gitlab.example.com" })).not.toBe(
      providerItemKey({ ...common, platformHost: "gitlab.internal.example" }),
    );
  });

  it("normalizes provider aliases and omitted default hosts", () => {
    expect(
      providerItemKey({
        provider: "gh",
        platformHost: "",
        owner: "acme",
        name: "widgets",
        number: 17,
      }),
    ).toBe(
      providerItemKey({
        provider: "github",
        platformHost: "github.com",
        owner: "acme",
        name: "widgets",
        number: 17,
      }),
    );
  });
});

describe("providerMutationKey", () => {
  it("shares a mutation family across provider aliases without mixing item kinds", () => {
    const common = {
      owner: "acme",
      name: "widgets",
      number: 17,
    };

    expect(providerMutationKey("pull", { ...common, provider: "gh", platformHost: "" }, "star")).toBe(
      providerMutationKey("pull", { ...common, provider: "github", platformHost: "github.com" }, "star"),
    );
    expect(
      providerMutationKey("issue", { ...common, provider: "github", platformHost: "github.com" }, "star"),
    ).not.toBe(providerMutationKey("pull", { ...common, provider: "github", platformHost: "github.com" }, "star"));
  });
});
