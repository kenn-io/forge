import { defineConfig } from "vite-plus";

export default defineConfig({
  run: {
    tasks: {
      "svelte-check:frontend": {
        command: "vp exec svelte-check --tsconfig ./tsconfig.json --fail-on-warnings",
        cwd: "frontend",
      },
      "svelte-check:ui": {
        command: "vp exec svelte-check --tsconfig ./tsconfig.json --fail-on-warnings",
        cwd: "packages/ui",
      },
    },
  },
  staged: {
    "*": "vp check --fix",
  },
  fmt: {
    printWidth: 120,
    sortImports: false,
  },
  lint: {
    ignorePatterns: [
      "frontend/scripts/**",
      "frontend/tests/**",
      "frontend/src/**/*.test.ts",
      "frontend/src/**/*.bench.test.ts",
      "packages/ui/src/api/generated/**",
      "packages/ui/src/api/roborev/generated/**",
      "packages/ui/src/**/*.test.ts",
      "packages/ui/src/**/*.bench.test.ts",
    ],
    rules: {
      // Oxlint does not yet model Svelte template writes reliably. Watch the
      // embedded-framework RFC for the eventual fix path; the direct open
      // evidence is a bind:this false positive in a related rule.
      // https://github.com/oxc-project/oxc/discussions/21936
      // https://github.com/oxc-project/oxc/issues/19470
      // https://github.com/oxc-project/oxc/issues/15761
      "eslint/no-unassigned-vars": "off",
      // These type-aware rules are useful for cleanup work, but enabling them
      // during the Vite+ migration would turn existing non-buggy code into
      // unrelated churn that the previous frontend check never enforced.
      "typescript/no-base-to-string": "off",
      "typescript/no-duplicate-type-constituents": "off",
      "typescript/no-floating-promises": "off",
      "typescript/no-redundant-type-constituents": "off",
      // Keep the migration scoped to tool consolidation; these style rules
      // disagree with existing readable test and store code but do not affect
      // the consistency gains from moving checks under Vite+.
      "unicorn/no-useless-fallback-in-spread": "off",
      "unicorn/prefer-string-starts-ends-with": "off",
    },
    options: { typeAware: true, typeCheck: true },
  },
});
