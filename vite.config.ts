import { defineConfig } from "vite-plus";

export default defineConfig({
  staged: {
    "*": "vp check --fix",
  },
  fmt: {
    printWidth: 80,
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
      // Svelte bind:this variables are assigned by the compiler, which Oxlint
      // cannot see; keep those bindings valid without local suppressions.
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
