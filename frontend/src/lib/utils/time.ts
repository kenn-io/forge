/**
 * Timestamp helpers for API data.
 *
 * Contract:
 * - The backend stores and emits absolute instants in UTC.
 * - These helpers preserve the instant when parsing.
 * - Local timezone conversion is presentation-only and must stay in explicit
 *   UI formatting helpers such as `localDateLabel()`.
 */

/**
 * Parses an API timestamp into a JavaScript Date without changing the instant.
 *
 * API payloads are expected to be UTC RFC3339 strings, but JavaScript will
 * also preserve the instant for older offset-formatted strings when tests
 * exercise legacy data.
 */
export function parseAPITimestamp(dateStr: string): Date {
  return new Date(dateStr);
}

/**
 * Converts an API timestamp to a local calendar label for display.
 *
 * This is one of the intentionally small number of places where frontend code
 * is allowed to apply the browser's local timezone. Callers that only need
 * ordering, filtering, or relative-time math should use `parseAPITimestamp()`
 * instead so they stay on the original UTC instant.
 */
export function localDateLabel(dateStr: string): string {
  return parseAPITimestamp(dateStr).toLocaleDateString();
}

/**
 * Converts an API timestamp to a local date and time label for display.
 */
export function localDateTimeLabel(dateStr: string): string {
  return parseAPITimestamp(dateStr).toLocaleString(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  });
}
