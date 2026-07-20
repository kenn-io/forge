# Workspace Agent Override Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Generate composed `AGENTS.override.md` files for Codex-family workspace launches while retaining additive `CLAUDE.local.md` files for Claude-family launches.

**Architecture:** Keep launch-context preparation in `internal/workspace/agent_context.go`. Select behavior with a trimmed, case-folded agent-name prefix; render Middleman's context once, then append readable root `AGENTS.md` bytes only for Codex. Move the existing marker ownership, atomic-write, and private Git-exclude contract directly to the new override path without retaining the obsolete Codex local-file path.

**Tech Stack:** Go standard library, SQLite-backed workspace tests, testify, Huma server test harness, Git private exclude files.

## Global Constraints

- Codex and Claude selection uses a case-folded prefix after trimming surrounding whitespace.
- Codex writes `AGENTS.override.md`; Claude writes `CLAUDE.local.md`; other agents write neither.
- Codex content is generated Middleman context first, followed by root `AGENTS.md` verbatim.
- Missing or unreadable root `AGENTS.md` produces a context-only override without blocking launch.
- Unmarked files, symlinks, and root instruction files remain untouched.
- Do not retain `AGENTS.local.md` as a fallback or alias, and do not delete existing copies.
- Invoke Go tests with `-shuffle=on`, without `-v` or `-count=1`.

---

### Task 1: Deliver workspace context through agent-native instruction files

**Files:**
- Modify: `internal/workspace/agent_context.go`
- Modify: `internal/workspace/agent_context_test.go`
- Modify: `internal/workspace/gitignore.go`
- Modify: `internal/workspace/gitignore_test.go`
- Modify: `internal/server/workspace_runtime_agent_context_test.go`
- Modify: `context/workspace-apis.md`

**Interfaces:**
- Consumes: `RenderAgentContext(AgentContext) string`, `generatedFileWritable(string) (bool, error)`, `EnsureGeneratedContextFilesIgnored(context.Context, string, []string) error`, and `writeGeneratedFileAtomic(string, string, []byte) error`.
- Produces: `agentContextRelPath(string) string` with family selection and `renderAgentInstructionFile(string, string, AgentContext) []byte` with Codex composition.

- [ ] **Step 1: Write the failing family-selection test**

Add to `internal/workspace/agent_context_test.go`:

```go
func TestAgentContextRelPathMatchesCaseFoldedAgentFamilyPrefixes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, targetKey, want string
	}{
		{name: "codex builtin", targetKey: "codex", want: "AGENTS.override.md"},
		{name: "codex configured suffix", targetKey: "Codex yolo", want: "AGENTS.override.md"},
		{name: "codex surrounding whitespace", targetKey: "  CODEX proxy  ", want: "AGENTS.override.md"},
		{name: "claude builtin", targetKey: "claude", want: "CLAUDE.local.md"},
		{name: "claude configured suffix", targetKey: "Claude reviewer", want: "CLAUDE.local.md"},
		{name: "unrelated agent", targetKey: "opencode"},
		{name: "prefix must begin name", targetKey: "my-codex"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, agentContextRelPath(tt.targetKey))
		})
	}
}
```

- [ ] **Step 2: Write the failing composition test**

Add beside the selector test. A directory at `AGENTS.md` exercises the owned unreadable-file fallback without testing operating-system permission behavior:

```go
func TestRenderAgentInstructionFile(t *testing.T) {
	t.Parallel()
	ctx := AgentContext{
		WorkspaceID: "ws-1", SourceKind: AgentSourceKindPullRequest,
		Provider: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget", ItemNumber: 42,
	}
	wantContext := RenderAgentContext(ctx)
	tests := []struct {
		name, relPath, agentsEntry, agentsContent, want string
	}{
		{
			name: "codex appends instructions verbatim", relPath: "AGENTS.override.md",
			agentsEntry: "file", agentsContent: "# Repository Rules\nkeep trailing bytes",
			want: wantContext + "\n# Repository Rules\nkeep trailing bytes",
		},
		{name: "codex missing instructions", relPath: "AGENTS.override.md", want: wantContext},
		{name: "codex unreadable instructions", relPath: "AGENTS.override.md", agentsEntry: "directory", want: wantContext},
		{name: "claude remains context only", relPath: "CLAUDE.local.md", agentsEntry: "file", agentsContent: "# Repository Rules\n", want: wantContext},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			switch tt.agentsEntry {
			case "file":
				require.NoError(t, os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(tt.agentsContent), 0o644))
			case "directory":
				require.NoError(t, os.Mkdir(filepath.Join(dir, "AGENTS.md"), 0o755))
			}
			assert.Equal(t, tt.want, string(renderAgentInstructionFile(dir, tt.relPath, ctx)))
		})
	}
}
```

- [ ] **Step 3: Move existing behavior tests to the override path**

In `internal/workspace/agent_context_test.go`, update the Codex ownership, symlink, refreshed-head, and legacy-head-repository cases to create, inspect, and ignore `AGENTS.override.md`. Keep Claude expectations on `CLAUDE.local.md`. Do not add an assertion that `AGENTS.local.md` stays absent; removal is established by the diff and passing behavior tests.

In `internal/workspace/gitignore_test.go`, replace requested and asserted Codex paths with `AGENTS.override.md` in the append, existing-ignore, clean-status, partial-request, fatal-error, and negation tests. The leading request and core assertions become:

```go
	require.NoError(EnsureGeneratedContextFilesIgnored(context.Background(), worktree, []string{
		"AGENTS.override.md", "CLAUDE.local.md",
	}))
	excludeText := readGitExclude(t, worktree)
	assert.Contains(excludeText, "/AGENTS.override.md")
	assert.Contains(excludeText, "/CLAUDE.local.md")
	assertGitIgnored(t, worktree, "AGENTS.override.md")
	assertGitIgnored(t, worktree, "CLAUDE.local.md")
```

- [ ] **Step 4: Add launch-path regression coverage**

In `TestLaunchWorkspaceRuntimeSessionPreparesAgentContext`, use target key `Codex yolo`. Seed tracked root instructions:

```go
	require.NoError(os.WriteFile(
		filepath.Join(worktree, "AGENTS.md"),
		[]byte("# Repository Agent Rules\n\nKeep this guidance.\n"), 0o644,
	))
	runServerWorkspaceTestGit(t, worktree, "add", "AGENTS.md")
	runServerWorkspaceTestGit(t, worktree, "commit", "-m", "add agent rules")
```

Read `AGENTS.override.md` after launch and assert the owned ordering contract:

```go
	override, err := os.ReadFile(filepath.Join(worktree, "AGENTS.override.md"))
	require.NoError(err)
	content := string(override)
	assert.Contains(content, "Source kind: pull request")
	assert.Contains(content, "PR: #42")
	assert.Less(strings.Index(content, "# Middleman Workspace Context"), strings.Index(content, "# Repository Agent Rules"))
	assert.True(strings.HasSuffix(content, "# Repository Agent Rules\n\nKeep this guidance.\n"))
	assertServerGitIgnored(t, worktree, "AGENTS.override.md")
```

Update the API-level Codex launch test to inspect and ignore `AGENTS.override.md`. Its workspace lacks root `AGENTS.md`, so the existing source and push-branch assertions also cover the context-only fallback through the HTTP route.

- [ ] **Step 5: Run focused tests and verify RED**

Run:

```bash
go test ./internal/workspace -run 'TestAgentContextRelPathMatchesCaseFoldedAgentFamilyPrefixes|TestRenderAgentInstructionFile|TestPrepareAgentLaunchContext|TestEnsureGeneratedContextFilesIgnored|TestGeneratedContextFilesDoNotDirtyGitStatus' -shuffle=on
go test ./internal/server -run 'TestLaunchWorkspaceRuntimeSessionPreparesAgentContext|TestWorkspaceRuntimeLaunchWritesAgentContextE2E' -shuffle=on
```

Expected: FAIL because `renderAgentInstructionFile` is undefined, family matching is exact, and the Git allowlist rejects `AGENTS.override.md`.

- [ ] **Step 6: Implement case-folded selection and Codex composition**

In `PrepareAgentLaunchContext`, replace the direct `RenderAgentContext` conversion with:

```go
	content := renderAgentInstructionFile(
		summary.WorktreePath, relPath, BuildAgentContext(*summary),
	)
	return writeGeneratedFileAtomic(summary.WorktreePath, relPath, content)
```

Replace exact selection and add:

```go
func agentContextRelPath(targetKey string) string {
	targetKey = strings.TrimSpace(targetKey)
	switch {
	case hasCaseFoldedPrefix(targetKey, "codex"):
		return "AGENTS.override.md"
	case hasCaseFoldedPrefix(targetKey, "claude"):
		return "CLAUDE.local.md"
	default:
		return ""
	}
}

func hasCaseFoldedPrefix(value, prefix string) bool {
	return len(value) >= len(prefix) && strings.EqualFold(value[:len(prefix)], prefix)
}

func renderAgentInstructionFile(worktreePath, relPath string, ctx AgentContext) []byte {
	content := []byte(RenderAgentContext(ctx))
	if relPath != "AGENTS.override.md" {
		return content
	}
	repositoryInstructions, err := os.ReadFile(filepath.Join(worktreePath, "AGENTS.md"))
	if err != nil || len(repositoryInstructions) == 0 {
		return content
	}
	content = append(content, '\n')
	return append(content, repositoryInstructions...)
}
```

- [ ] **Step 7: Replace the generated-path allowlist entry**

In `generatedContextIgnorePattern` use:

```go
	switch clean {
	case "AGENTS.override.md", "CLAUDE.local.md":
		return clean, "/" + clean, nil
	default:
		return "", "", fmt.Errorf("unknown generated context path: %s", clean)
	}
```

Do not add an `AGENTS.local.md` compatibility case and do not clean historical private-exclude lines.

- [ ] **Step 8: Run focused tests and verify GREEN**

Run the same commands from Step 5.

Expected: PASS with the family, composition, ownership, Git safety, and launch contracts covered.

- [ ] **Step 9: Update durable workspace context**

Replace the opening paragraph under `## Agent Launch Context` in `context/workspace-apis.md` with:

```markdown
Agent launch selects Codex and Claude families by case-folded target-name prefix.
Codex receives generated workspace context followed by root `AGENTS.md` verbatim
in `AGENTS.override.md`; missing or unreadable root instructions yield a
context-only override. Claude receives context-only `CLAUDE.local.md` because its
local file is additive (`internal/workspace/agent_context.go::agentContextRelPath`).
```

Keep the ownership paragraph's prohibition on modifying root instruction files while naming `AGENTS.override.md` as the generated Codex target.

- [ ] **Step 10: Verify the affected suites and removal**

Run:

```bash
go test ./internal/workspace ./internal/server -shuffle=on
scripts/context-sync --check
rg -n 'AGENTS\.local\.md' internal/workspace internal/server context/workspace-apis.md
git diff --check
git status --short
```

Expected: both Go packages PASS; context structure passes; `rg` returns no matches in the changed runtime, test, and context surfaces; the diff check succeeds; status lists only intended files. The `rg` command is a one-time removal check, not a permanent absence test.

- [ ] **Step 11: Commit the implementation**

Invoke `context-sync --commit` and the mandatory commit skill. Stage only the six task files and create `fix: deliver workspace context through Codex overrides`. Explain in the body that Codex replacement semantics differ from Claude's additive semantics and that missing repository instructions deliberately degrade to context-only behavior.
