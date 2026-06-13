# e2e-seed fixtures

These JSON files are verbatim API responses captured from the seeded e2e Go
server (`cmd/e2e-server`), with only the `$schema` keys stripped (they embed
the server's ephemeral port). Vitest component tests import them so their
data matches what the real backend returns for the standard seed
(acme/widgets, acme/tools on github.com, plus the GitLab read-only fixture).

## Regenerating

If the seed data in `cmd/e2e-server` or the API response shapes change,
recapture from a running instance:

```sh
go run ./cmd/e2e-server -port 0 -server-info-file /tmp/e2e-info.json
# read base_url from /tmp/e2e-info.json, then e.g.:
curl -s "$BASE/api/v1/pulls" > pulls.json
curl -s "$BASE/api/v1/pulls/github/acme/widgets/1" > pull-widgets-1.json
curl -s "$BASE/api/v1/pulls/github/acme/tools/11/stack" > stack-tools-11.json
curl -s "$BASE/api/v1/host/gitlab.example.com/issues/gl/group/project/11" > issue-gitlab-11.json
```

Strip `$schema` keys, run the formatter (`vp check --fix` on this directory),
and re-run the consuming tests — assertions pin seed-specific values
(titles, counts, stack ordering), so genuine drift fails loudly rather than
silently.

Drift guard: `frontend/src/lib/testing/e2e-server-pool.test.ts` boots the
real server in every Vitest run, and the Playwright e2e-full suite still
exercises the live API for the flows that stayed browser-based. If a fixture
here no longer matches the server, the corresponding spec-level coverage is
the place to catch it; when touching seed data, regenerate rather than
hand-editing these files.
