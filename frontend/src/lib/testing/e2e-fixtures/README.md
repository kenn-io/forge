# e2e-seed fixtures

Verbatim API responses captured from the seeded e2e Go server
(`cmd/e2e-server`) with `$schema` keys stripped. See
`packages/ui/src/testing/e2e-fixtures/README.md` for the capture and
regeneration procedure; these files cover the frontend-side tests
(`/api/v1/repos`, `/api/v1/settings`) and must be recaptured the same way
when the seed or response shapes change.
