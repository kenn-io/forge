import type { Elicitation } from "./chat-types.js";

// Form model for the ACP/MCP restricted elicitation schema: a flat object of
// primitive properties. Anything outside that vocabulary degrades to a plain
// text field whose string value is sent as typed.

export type ElicitationChoice = { value: string; label: string };
export type ElicitationTextFormat = "email" | "uri" | "date" | "date-time";
type FieldBase = { key: string; label: string; description: string; required: boolean };
export type ElicitationField =
  | (FieldBase & {
      kind: "text";
      format: ElicitationTextFormat | undefined;
      minLength: number | undefined;
      maxLength: number | undefined;
      initial: string;
    })
  | (FieldBase & {
      kind: "number";
      integer: boolean;
      minimum: number | undefined;
      maximum: number | undefined;
      initial: string;
    })
  | (FieldBase & { kind: "boolean"; initial: boolean })
  | (FieldBase & { kind: "select"; choices: ElicitationChoice[]; initial: string })
  | (FieldBase & {
      kind: "multi";
      choices: ElicitationChoice[];
      minItems: number | undefined;
      maxItems: number | undefined;
      initial: string[];
    });
export type ElicitationValue = string | boolean | string[];
export type ElicitationValues = Record<string, ElicitationValue>;
export type ElicitationResponse =
  | { action: "accept"; content: Record<string, unknown> }
  | { action: "decline" | "cancel" };

type Json = Record<string, unknown>;

function record(value: unknown): Json | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? (value as Json) : undefined;
}

function text(value: unknown): string | undefined {
  return typeof value === "string" ? value : undefined;
}

function finite(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function stringList(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];
}

// Single and multi-select schemas spell choices as `enum` (+ optional
// `enumNames`) or as `oneOf`/`anyOf` lists of `{const, title}`.
function choicesOf(schema: Json): ElicitationChoice[] | undefined {
  if (Array.isArray(schema.enum)) {
    const names: unknown[] = Array.isArray(schema.enumNames) ? schema.enumNames : [];
    return schema.enum.flatMap((value: unknown, index) => {
      if (typeof value !== "string") return [];
      return [{ value, label: text(names[index]) || value }];
    });
  }
  const titled = Array.isArray(schema.oneOf) ? schema.oneOf : Array.isArray(schema.anyOf) ? schema.anyOf : undefined;
  if (!titled) return undefined;
  return titled.flatMap((entry) => {
    const option = record(entry);
    const value = text(option?.const);
    if (value === undefined) return [];
    return [{ value, label: text(option?.title) || value }];
  });
}

function textFormat(value: unknown): ElicitationTextFormat | undefined {
  return value === "email" || value === "uri" || value === "date" || value === "date-time" ? value : undefined;
}

export function elicitationFields(schema: Elicitation["schema"]): ElicitationField[] {
  const required = new Set(schema.required ?? []);
  return Object.entries(schema.properties ?? {}).map(([key, raw]): ElicitationField => {
    const property = record(raw) ?? {};
    const base: FieldBase = {
      key,
      label: text(property.title) || key,
      description: text(property.description) ?? "",
      required: required.has(key),
    };
    const fallback = property.default;
    if (property.type === "boolean") {
      return { ...base, kind: "boolean", initial: fallback === true };
    }
    if (property.type === "number" || property.type === "integer") {
      const initial = finite(fallback);
      return {
        ...base,
        kind: "number",
        integer: property.type === "integer",
        minimum: finite(property.minimum),
        maximum: finite(property.maximum),
        initial: initial === undefined ? "" : String(initial),
      };
    }
    if (property.type === "array") {
      const choices = choicesOf(record(property.items) ?? {}) ?? [];
      const allowed = new Set(choices.map((choice) => choice.value));
      return {
        ...base,
        kind: "multi",
        choices,
        minItems: finite(property.minItems),
        maxItems: finite(property.maxItems),
        initial: stringList(fallback).filter((value) => allowed.has(value)),
      };
    }
    const choices = property.type === "string" ? choicesOf(property) : undefined;
    if (choices) {
      const initial = text(fallback) ?? "";
      return {
        ...base,
        kind: "select",
        choices,
        initial: choices.some((choice) => choice.value === initial) ? initial : "",
      };
    }
    const isString = property.type === "string";
    return {
      ...base,
      kind: "text",
      format: isString ? textFormat(property.format) : undefined,
      minLength: isString ? finite(property.minLength) : undefined,
      maxLength: isString ? finite(property.maxLength) : undefined,
      initial: text(fallback) ?? "",
    };
  });
}

export function initialValues(fields: readonly ElicitationField[]): ElicitationValues {
  return Object.fromEntries(fields.map((field) => [field.key, field.initial]));
}

function textValue(values: ElicitationValues, key: string): string {
  const value = values[key];
  return typeof value === "string" ? value : "";
}

function listValue(values: ElicitationValues, key: string): string[] {
  const value = values[key];
  return Array.isArray(value) ? value : [];
}

function isEmpty(field: ElicitationField, values: ElicitationValues): boolean {
  if (field.kind === "boolean") return false;
  if (field.kind === "multi") return listValue(values, field.key).length === 0;
  return textValue(values, field.key).trim() === "";
}

const emailPattern = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
const datePattern = /^\d{4}-\d{2}-\d{2}$/;
const dateTimePattern = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2}(\.\d+)?)?(Z|[+-]\d{2}:\d{2})$/i;

function isURL(value: string): boolean {
  try {
    new URL(value);
    return true;
  } catch {
    return false;
  }
}

function formatProblem(format: ElicitationTextFormat | undefined, value: string): string {
  switch (format) {
    case "email":
      return emailPattern.test(value) ? "" : "Enter an email address.";
    case "uri":
      return isURL(value) ? "" : "Enter a full URL.";
    case "date":
      return datePattern.test(value) && !Number.isNaN(Date.parse(value)) ? "" : "Enter a date as YYYY-MM-DD.";
    case "date-time":
      return dateTimePattern.test(value) && !Number.isNaN(Date.parse(value))
        ? ""
        : "Enter a date and time such as 2026-01-31T09:00:00Z.";
    default:
      return "";
  }
}

// Problem with a filled-in value. Empty fields report nothing here; required
// gating happens in canSubmit so untouched fields are not flagged as errors.
export function fieldProblem(field: ElicitationField, values: ElicitationValues): string {
  if (isEmpty(field, values)) return "";
  if (field.kind === "text") {
    const value = textValue(values, field.key);
    const length = Array.from(value).length;
    if (field.minLength !== undefined && length < field.minLength)
      return `Enter at least ${field.minLength} characters.`;
    if (field.maxLength !== undefined && length > field.maxLength)
      return `Enter at most ${field.maxLength} characters.`;
    return formatProblem(field.format, value);
  }
  if (field.kind === "number") {
    const value = Number(textValue(values, field.key).trim());
    if (!Number.isFinite(value)) return "Enter a number.";
    if (field.integer && !Number.isInteger(value)) return "Enter a whole number.";
    if (field.minimum !== undefined && value < field.minimum) return `Enter ${field.minimum} or more.`;
    if (field.maximum !== undefined && value > field.maximum) return `Enter ${field.maximum} or less.`;
    return "";
  }
  if (field.kind === "multi") {
    const count = listValue(values, field.key).length;
    if (field.minItems !== undefined && count < field.minItems) return `Choose at least ${field.minItems}.`;
    if (field.maxItems !== undefined && count > field.maxItems) return `Choose at most ${field.maxItems}.`;
  }
  return "";
}

export function canSubmit(fields: readonly ElicitationField[], values: ElicitationValues): boolean {
  return fields.every((field) => (isEmpty(field, values) ? !field.required : fieldProblem(field, values) === ""));
}

// Accepted content: numbers coerced, booleans always present (a checkbox has
// no unset state), and empty optional fields omitted.
export function elicitationContent(
  fields: readonly ElicitationField[],
  values: ElicitationValues,
): Record<string, unknown> {
  const content: Record<string, unknown> = {};
  for (const field of fields) {
    if (field.kind === "boolean") {
      content[field.key] = values[field.key] === true;
      continue;
    }
    if (isEmpty(field, values)) continue;
    if (field.kind === "multi") content[field.key] = listValue(values, field.key);
    else if (field.kind === "number") content[field.key] = Number(textValue(values, field.key).trim());
    else content[field.key] = textValue(values, field.key);
  }
  return content;
}
