import assert from "node:assert/strict";
import test from "node:test";

import { renderModule, schemaConstraints } from "./generate-schema-constraints.mjs";

test("extracts only numeric bounds, sorted by schema and property", () => {
  const constraints = schemaConstraints({
    components: {
      schemas: {
        Zeta: { properties: { name: { type: "string" } } },
        Alpha: {
          properties: {
            upper: { type: "integer", maximum: 5 },
            both: { type: "integer", minimum: 10, maximum: 250 },
            plain: { type: "boolean" },
          },
        },
      },
    },
  });
  assert.deepEqual(constraints, {
    Alpha: { both: { minimum: 10, maximum: 250 }, upper: { maximum: 5 } },
  });
});

test("renders a const module the frontend can import", () => {
  const rendered = renderModule({ Alpha: { both: { minimum: 10, maximum: 250 } } });
  assert.match(rendered, /export const schemaConstraints = \{/);
  assert.match(rendered, /Alpha: \{\n    both: \{ minimum: 10, maximum: 250 \},\n  \},/);
  assert.match(rendered, /\} as const;\n$/);
});
