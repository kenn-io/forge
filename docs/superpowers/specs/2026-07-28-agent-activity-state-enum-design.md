# Agent Activity State Enum Design

## Goal

Represent agent activity states as a real ordinal Go enum so priority is part of the type definition instead of a separate switch, without changing persisted reports or API values.

## Design

Change `agentactivity.State` from a string alias to an unsigned integer type. Declare the states with `iota` in ascending priority order: unknown, idle, done, working, input, approval. `StateUnknown` is an internal-only zero-value sentinel; valid state priority is the enum's numeric value, while the sentinel and out-of-range values have priority zero.

Keep the external representation stable. `String`, `MarshalJSON`, and `UnmarshalJSON` map the five externally valid states to the existing lowercase strings. `MarshalJSON` rejects the sentinel and out-of-range values; `UnmarshalJSON` rejects `"unknown"`, other unknown strings, and numeric JSON. Workspace API responses continue converting valid enum values through `String`, so the OpenAPI contract remains unchanged.

## Error Handling

Invalid enum values cannot be written as valid reports. Invalid JSON state values cause report decoding to fail, preserving the current behavior of ignoring malformed report files.

## Testing

Add focused coverage that pins enum ordering, string conversion, JSON string round trips, sentinel and out-of-range serialization rejection, and invalid JSON rejection. Existing agent lifecycle and workspace HTTP tests continue to prove that hook events and API responses retain their current values.
