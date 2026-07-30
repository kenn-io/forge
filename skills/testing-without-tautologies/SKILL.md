---
name: testing-without-tautologies
description: Use when creating, editing, fixing, or reviewing tests; when adding mocks, assertions, smoke tests, unit tests, integration tests, e2e tests, or changing tests after failures.
---

# Testing Without Tautologies

## Core Idea

Tests should fail when protected behavior breaks. A passing test helps only if it can catch a real problem.

Before writing or changing a test, ask: "What production change should make this test fail?" If you cannot answer, redesign the test.

## Quality Gate

Before writing the test body, answer these:

- **Who uses this?** Prefer public APIs, HTTP contracts, UI, CLI output, persisted data, or caller-visible results. Avoid private state.
- **What example proves it?** Use concrete inputs and literal expected outputs. Do not compute expected values with production logic.
- **What break would this catch?** Name the wrong branch, missing side effect, wrong argument, boundary case, or contract violation.
- **Do we own it?** Test our choices at framework, SDK, database, and service boundaries. Do not re-test documented dependency mechanics.
- **Can you state it?** Given this setup, when the user/system does X, then Y observable behavior changes. If Y is not assertable, the test is not ready.

## Required Checks

Apply these checks to every new or modified test:

1. **Assert observable effects**
   - Check returned values, persisted state, UI, events, API calls, errors, permissions, or other visible effects.
   - A no-assertion test is acceptable only when the failure mode is the subject, such as "this constructor rejects invalid input." Prefer explicit assertions anyway.

2. **Make mocks specific**
   - Verify arguments, call counts, order, and branches when they are part of the contract.
   - Do not let a mock accept any input when the code must pass one value.

3. **Separate branch doubles**
   - Do not reuse one mock handler for success, error, incomplete, unauthorized, or other mutually exclusive paths.
   - Give each branch its own spy/mock so the wrong branch cannot satisfy the expectation.

4. **Do not mock the subject**
   - Mock dependencies, boundaries, and slow or nondeterministic collaborators.
   - Do not replace the method, component, handler, query, reducer, or workflow under test.
   - Prefer real in-process collaborators and framework test utilities when they keep the test fast.

5. **Investigate failures before changing expectations**
   - Do not flip expected values just to make a failing test pass.
   - First decide whether the production change is intended. Then update the test to describe the new contract.

6. **Avoid mirror assertions**
   - Do not compute expected values with the same logic under test.
   - Use literals, hand-checked fixtures, small examples, or invariant/property assertions.
   - Keep test logic simple enough to review by inspection.

7. **Do not test upstream functionality**
   - Do not prove that a framework, SDK, standard library, database, parser, router, or generated client works as documented.
   - Example: do not test Huma URL params, query strings, status codes, or OpenAPI wiring unless your code adds behavior there.
   - Test your boundary contract instead: route registration, value handoff to domain code, errors, permissions, and response shape.
   - For surprising upstream behavior, write a narrow characterization test around your integration point. Name the upstream assumption.
   - For fake external services, test consumer behavior and prefer a contract/verified fake check for the fake itself.

8. **Avoid blindingly obvious current-code assertions**
   - Do not test that the implementation is written the way it is written now.
   - Skip tests for plain constructor assignment, getters, trivial forwarding, constants, and data-only structs.
   - Test them only when they validate, normalize, default, derive, copy, enforce permissions, handle errors, cause side effects, or protect compatibility.
   - Prefer the first consumer-visible result that depends on the fields.

## Test Level

Use the narrowest test that can catch the break, but honor repo rules. In kenn-forge, e2e tests are required for API behavior, data flow across HTTP and SQLite, and user-visible workflows. Keep those e2e tests non-tautological: assert the workflow result, stored state, rendered UI, or API contract, not just that the server, router, or page did not crash.

## Mutation Check

Before finishing, mentally mutate the production code. At least one relevant test should fail for each realistic mutation.

- Wrong constant or argument.
- Wrong branch handler.
- Missing state change.
- Empty/default return.
- Missing side effect.
- Broken fake at a boundary your code should notice.
- Renamed or rearranged private fields with behavior preserved.
- Missing validation for zero, empty, nil, unauthorized, or malformed input.

If none fail, the test is probably tautological.

## Red Flags

- Reuses the same setup/assertion object, guaranteeing equality.
- Can fail only through panic, exception, missing selector, or server crash.
- Still matters if only the framework/library remains.
- Translates a constructor, getter, setter, mapper, or wrapper line by line.
- Exists for coverage without checking side effects, boundaries, or outcomes.
- Hides expected values behind loops, formatters, builders, or helpers.
