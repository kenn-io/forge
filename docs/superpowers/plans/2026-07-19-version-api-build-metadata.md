# Version API Build Metadata Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expand `GET /api/v1/version` to return the same four build metadata fields as `middleman version --json`.

**Architecture:** Store an immutable `BuildInfo` value on `Server`, set it from the existing linker-provided variables during startup, and return it through the existing Huma route. Keep the route and its generated operation unchanged while expanding the response schema in place.

**Tech Stack:** Go, Huma, `net/http/httptest`, Testify, OpenAPI, oapi-codegen, openapi-typescript.

## Global Constraints

- Preserve `GET /api/v1/version` and its existing authentication behavior.
- Return required string fields named `name`, `version`, `commit`, and `buildDate`.
- Use raw linker-provided version and commit values; do not synthesize `dev-<commit>`.
- Add one focused server contract test only; do not add e2e or browser coverage.
- Regenerate checked-in OpenAPI, Go client, and TypeScript client artifacts with `make api-generate`.

---

### Task 1: Return Complete Build Metadata

**Files:**
- Modify: `internal/server/api_test.go`
- Modify: `internal/server/server.go`
- Modify: `cmd/middleman/main.go`
- Regenerate: `frontend/openapi/openapi.yaml`
- Regenerate: `internal/apiclient/spec/openapi.json`
- Regenerate: `packages/ui/src/api/generated/schema.ts`
- Regenerate: `internal/apiclient/generated/client.gen.go`

**Interfaces:**
- Consumes: linker-populated package variables `version`, `commit`, and `buildDate` in `cmd/middleman/main.go`.
- Produces: `server.BuildInfo`, `(*server.Server).SetBuildInfo(server.BuildInfo)`, and the expanded `GET /api/v1/version` JSON response.

- [ ] **Step 1: Write the failing API contract test**

Add this focused test to `internal/server/api_test.go`:

```go
func TestAPIGetVersionReturnsBuildMetadata(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.SetBuildInfo(BuildInfo{
		Name:      "middleman",
		Version:   "1.2.3",
		Commit:    "abc1234",
		BuildDate: "2026-07-12T12:00:00Z",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var body map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert := assert.New(t)
	assert.Equal("middleman", body["name"])
	assert.Equal("1.2.3", body["version"])
	assert.Equal("abc1234", body["commit"])
	assert.Equal("2026-07-12T12:00:00Z", body["buildDate"])
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
go test ./internal/server -run '^TestAPIGetVersionReturnsBuildMetadata$' -shuffle=on
```

Expected: FAIL to compile because `BuildInfo` and `SetBuildInfo` do not exist yet.

- [ ] **Step 3: Add the build metadata model and handler wiring**

Replace the narrow response type in `internal/server/server.go` with:

```go
type BuildInfo struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"buildDate"`
}

type versionOutputBody BuildInfo
type versionOutput = bodyOutput[versionOutputBody]
```

Rename the `Server.version string` field to:

```go
buildInfo BuildInfo
```

Replace `SetVersion` with:

```go
// SetBuildInfo sets the metadata returned by GET /api/v1/version.
func (s *Server) SetBuildInfo(info BuildInfo) { s.buildInfo = info }
```

Return the stored value from the handler:

```go
func (s *Server) getVersion(
	_ context.Context, _ *struct{},
) (*versionOutput, error) {
	resp := &versionOutput{}
	resp.Body = versionOutputBody(s.buildInfo)
	return resp, nil
}
```

- [ ] **Step 4: Pass the CLI build metadata into the server**

In `cmd/middleman/main.go`, replace the display-version construction and `SetVersion` call with:

```go
srv.SetBuildInfo(server.BuildInfo{
	Name:      "middleman",
	Version:   version,
	Commit:    commit,
	BuildDate: buildDate,
})
```

- [ ] **Step 5: Run the focused test and verify GREEN**

Run:

```bash
go test ./internal/server -run '^TestAPIGetVersionReturnsBuildMetadata$' -shuffle=on
```

Expected: PASS.

- [ ] **Step 6: Regenerate and inspect the API artifacts**

Run:

```bash
make api-generate
git diff -- frontend/openapi/openapi.yaml internal/apiclient/spec/openapi.json packages/ui/src/api/generated/schema.ts internal/apiclient/generated/client.gen.go
```

Expected: the version response schema gains required `name`, `commit`, and `buildDate` string fields; no unrelated operation changes appear.

- [ ] **Step 7: Run focused final verification**

Run:

```bash
go test ./internal/server -run '^(TestAPIGetVersionReturnsBuildMetadata|TestOpenAPIDocumentsCustomStatusCodes)$' -shuffle=on
go test ./cmd/middleman -run '^TestRunCLIVersion' -shuffle=on
git diff --check
```

Expected: both Go test commands pass and `git diff --check` exits successfully.

- [ ] **Step 8: Commit the implementation**

Before committing, invoke the repository-local `context-sync` skill with `--commit`, then the mandatory commit skill. Stage only the implementation, test, and generated API files and create a new commit without amending:

```bash
git add cmd/middleman/main.go internal/server/server.go internal/server/api_test.go frontend/openapi/openapi.yaml internal/apiclient/spec/openapi.json packages/ui/src/api/generated/schema.ts internal/apiclient/generated/client.gen.go
git commit -m "feat: expose complete server build metadata"
```
