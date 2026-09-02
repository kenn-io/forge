// Emit numeric schema constraints from the generated OpenAPI document as a
// TypeScript module. openapi-typescript keeps types but drops `minimum` and
// `maximum`, so the frontend could not validate a bounded field without
// duplicating the server's limits by hand. This module is the single source
// the UI reads; regenerate it with `make api-generate`.

import { readFileSync, writeFileSync } from "node:fs";

export function schemaConstraints(document) {
  const schemas = document?.components?.schemas ?? {};
  const out = {};
  for (const schemaName of Object.keys(schemas).sort()) {
    const properties = schemas[schemaName]?.properties ?? {};
    const fields = {};
    for (const propertyName of Object.keys(properties).sort()) {
      const property = properties[propertyName] ?? {};
      const constraint = {};
      if (typeof property.minimum === "number") constraint.minimum = property.minimum;
      if (typeof property.maximum === "number") constraint.maximum = property.maximum;
      if (Object.keys(constraint).length > 0) fields[propertyName] = constraint;
    }
    if (Object.keys(fields).length > 0) out[schemaName] = fields;
  }
  return out;
}

export function renderModule(constraints) {
  const lines = [
    "/**",
    " * This file was auto-generated from internal/apiclient/spec/openapi.json.",
    " * Do not make direct changes to the file.",
    " */",
    "",
    "export const schemaConstraints = {",
  ];
  for (const [schemaName, fields] of Object.entries(constraints)) {
    lines.push(`  ${schemaName}: {`);
    for (const [propertyName, constraint] of Object.entries(fields)) {
      const parts = Object.entries(constraint).map(([key, value]) => `${key}: ${value}`);
      lines.push(`    ${propertyName}: { ${parts.join(", ")} },`);
    }
    lines.push("  },");
  }
  lines.push("} as const;", "");
  return lines.join("\n");
}

const invokedDirectly = process.argv[1] && import.meta.url === new URL(`file://${process.argv[1]}`).href;
if (invokedDirectly) {
  const [specPath, outPath] = process.argv.slice(2);
  if (!specPath || !outPath) {
    console.error("usage: generate-schema-constraints.mjs <openapi.json> <out.ts>");
    process.exit(2);
  }
  const document = JSON.parse(readFileSync(specPath, "utf8"));
  writeFileSync(outPath, renderModule(schemaConstraints(document)));
}
