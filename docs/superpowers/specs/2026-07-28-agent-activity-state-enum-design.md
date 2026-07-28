# Agent Activity State Enum Design

## Goal

Represent agent activity states as a real ordinal Go enum so priority is part of the type definition instead of a separate switch, without changing persisted reports or API values.

## Design

Change `agentactivity.State` from a string alias to an unsigned integer type. Declare the states with `iota` in ascending priority order: unknown, idle, done, working, input, approval. Valid state priority is therefore the enum's numeric value; unknown and out-of-range values have priority zero.

Keep the external representation stable. `String`, `MarshalJSON`, and `UnmarshalJSON` map valid enum values to the existing lowercase strings. Unknown strings and numeric JSON are rejected when reading reports. Workspace API responses continue converting the enum through `String`, so the OpenAPI contract remains unchanged.

## Error Handling

Invalid enum values cannot be written as valid reports. Invalid JSON state values cause report decoding to fail, preserving the current behavior of ignoring malformed report files.

## Testing

Add focused coverage that pins enum ordering, string conversion, JSON string round trips, and invalid JSON rejection. Existing agent lifecycle and workspace HTTP tests continue to prove that hook events and API responses retain their current values.
