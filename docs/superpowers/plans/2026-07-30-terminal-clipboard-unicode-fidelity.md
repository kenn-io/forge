# Terminal Clipboard Unicode Fidelity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Preserve em dashes, non-breaking spaces, and other Unicode text when tmux clipboard writes fall back to macOS `pbcopy`.

**Architecture:** Keep the existing synchronous OSC 52 parser, trusted-gesture authorization, browser clipboard attempts, and loopback fallback unchanged. Widen the native clipboard command-runner seam so macOS can pass an explicit `LC_ALL=en_US.UTF-8` child environment while every other platform continues inheriting its environment normally.

**Tech Stack:** Go, `os/exec`, testify, TypeScript, Playwright, xterm.js, tmux.

## Global Constraints

- Keep `frontend/src/lib/components/terminal/osc52Clipboard.ts`, `terminalClipboardWriter.ts`, and the current `XtermTerminalPane.svelte` clipboard flow unchanged.
- Do not add `@xterm/addon-clipboard`, `js-base64`, another dependency, or a compatibility adapter.
- Apply `LC_ALL=en_US.UTF-8` only to macOS `pbcopy`; do not apply it to `wl-copy`, `xclip`, `xsel`, or `clip.exe`.
- Pass the original Unicode string unchanged to `pbcopy`.
- Preserve Windows UTF-16LE input.
- Run direct Go tests with `-shuffle=on`, without `-count=1` or `-v`.
- Use Vite+ directly for frontend checks and tests; never use npm.

## File Map

- Modify `internal/systemclipboard/systemclipboard.go`: carry an optional child environment through `commandRunner`, construct the macOS UTF-8 environment, and apply it in `runCommand`.
- Modify `internal/systemclipboard/systemclipboard_test.go`: observe the widened runner seam, prove the macOS locale override and unchanged Unicode input, and prove non-macOS commands receive no environment override.
- Modify `frontend/tests/e2e-full/00-tmux-browser-clipboard.spec.ts`: exercise em dash and non-breaking-space text through real tmux drag-copy in Chromium and Firefox.
- Modify `context/workspace-runtime-lifecycle.md`: anchor the macOS clipboard locale invariant after implementation.

---

### Task 1: Force UTF-8 at the macOS clipboard process boundary

**Files:**
- Modify: `internal/systemclipboard/systemclipboard_test.go`
- Modify: `internal/systemclipboard/systemclipboard.go`
- Modify: `context/workspace-runtime-lifecycle.md`

**Interfaces:**
- Consumes: `nativeWriter.WriteText(context.Context, string) error`.
- Produces: `commandRunner(context.Context, string, []string, []string, string) error`, where the fourth argument is a complete child environment or `nil` to inherit the parent environment.
- Produces: `environmentWithOverride([]string, string, string) []string`, which returns a copy with every existing assignment for the named key replaced by one final assignment.

- [ ] **Step 1: Widen the fake runner in the table test and add platform environment expectations**

Update the table's macOS case to use the exact Unicode characters that previously became mojibake:

```go
{
	name:      "macOS",
	goos:      "darwin",
	paths:     map[string]string{"pbcopy": "/usr/bin/pbcopy"},
	wantName:  "/usr/bin/pbcopy",
	text:      "accountability — no access\u00a0",
	wantInput: "accountability — no access\u00a0",
},
```

Inside each subtest, set a conflicting locale and a sentinel inherited value:

```go
t.Setenv("LC_ALL", "C")
t.Setenv("MIDDLEMAN_CLIPBOARD_TEST_ENV", "preserved")

var gotEnvironment []string
var gotInput string
```

Change the fake runner to accept and capture the child environment:

```go
run: func(
	_ context.Context,
	name string,
	args []string,
	environment []string,
	text string,
) error {
	assert.Equal(tt.wantName, name)
	assert.Equal(tt.wantArgs, args)
	gotEnvironment = environment
	gotInput = text
	return nil
},
```

After `WriteText`, retain the exact input assertion and add:

```go
if tt.goos == "darwin" {
	assert.Equal("en_US.UTF-8", environmentValue(gotEnvironment, "LC_ALL"))
	assert.Equal(
		"preserved",
		environmentValue(gotEnvironment, "MIDDLEMAN_CLIPBOARD_TEST_ENV"),
	)
	assert.Equal(1, environmentKeyCount(gotEnvironment, "LC_ALL"))
} else {
	assert.Nil(gotEnvironment)
}
```

Add test-only helpers at the bottom of the file:

```go
func environmentValue(environment []string, key string) string {
	prefix := key + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}

func environmentKeyCount(environment []string, key string) int {
	prefix := key + "="
	count := 0
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			count++
		}
	}
	return count
}
```

Import `strings`. Widen the unused runner in `TestNativeWriterReportsUnavailableClipboard` with the same `[]string` environment argument so the test file describes the intended seam consistently.

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
go test ./internal/systemclipboard -run TestNativeWriterSelectsPlatformClipboardCommand -shuffle=on
```

Expected: compilation fails because `commandRunner` and `nativeWriter.WriteText` do not yet carry the environment argument. This is the missing process-boundary behavior, not a test typo.

- [ ] **Step 3: Widen `commandRunner` and select the macOS-only child environment**

Change the runner type to:

```go
type commandRunner func(
	context.Context,
	string,
	[]string,
	[]string,
	string,
) error
```

In `nativeWriter.WriteText`, preserve the existing input encoding and add a separate optional environment:

```go
input := text
if w.goos == "windows" {
	input = encodeUTF16LE(text)
}

var environment []string
if w.goos == "darwin" {
	environment = environmentWithOverride(
		os.Environ(),
		"LC_ALL",
		"en_US.UTF-8",
	)
}

if err := w.run(
	ctx,
	command.name,
	command.args,
	environment,
	input,
); err != nil {
	return fmt.Errorf("write system clipboard: %w", err)
}
```

Add the pure environment helper:

```go
func environmentWithOverride(
	environment []string,
	key string,
	value string,
) []string {
	prefix := key + "="
	overridden := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			overridden = append(overridden, entry)
		}
	}
	return append(overridden, prefix+value)
}
```

Widen `runCommand` and assign the environment only when the caller provided one:

```go
func runCommand(
	ctx context.Context,
	name string,
	args []string,
	environment []string,
	text string,
) error {
	release, err := procutil.TryAcquire(
		ctx,
		"clipboard subprocess capacity",
	)
	if err != nil {
		return err
	}
	defer release()

	command := procutil.CommandContext(ctx, name, args...)
	if environment != nil {
		command.Env = environment
	}
	command.Stdin = strings.NewReader(text)
	return command.Run()
}
```

- [ ] **Step 4: Format and verify GREEN**

Run:

```bash
gofmt -w internal/systemclipboard/systemclipboard.go internal/systemclipboard/systemclipboard_test.go
go test ./internal/systemclipboard -shuffle=on
```

Expected: PASS. The macOS table row observes one `LC_ALL=en_US.UTF-8`, preserves the sentinel environment value and exact Unicode input, while Wayland, X11, and Windows observe a `nil` override.

- [ ] **Step 5: Record the implemented runtime invariant**

Add this bullet beside the existing Windows clipboard invariant in `context/workspace-runtime-lifecycle.md`:

```markdown
- macOS loopback clipboard fallback must run `pbcopy` with `LC_ALL=en_US.UTF-8`; service launchers may omit
  a UTF-8 locale and make `pbcopy` reinterpret unchanged UTF-8 input
  (`internal/systemclipboard/systemclipboard.go::nativeWriter.WriteText`).
```

- [ ] **Step 6: Run context validation and commit Task 1**

Invoke the repository-local `context-sync` skill with `--commit`, then the mandatory commit skill. Run:

```bash
scripts/context-sync --check
git add internal/systemclipboard/systemclipboard.go \
  internal/systemclipboard/systemclipboard_test.go \
  context/workspace-runtime-lifecycle.md
git commit -m "fix: preserve Unicode in macOS clipboard fallback"
```

The commit body must explain that `pbcopy` chooses input encoding from its inherited locale and that the override is intentionally macOS-only.

---

### Task 2: Prove Unicode reaches both browser and fallback clipboard boundaries

**Files:**
- Modify: `frontend/tests/e2e-full/00-tmux-browser-clipboard.spec.ts`

**Interfaces:**
- Consumes: the existing `renderMarker`, `dragTerminalCells`, `fallbackWrites`, and browser clipboard helpers in the real-tmux full-stack suite.
- Produces: no production interface; adds cross-browser characterization of the unchanged frontend clipboard flow.

- [ ] **Step 1: Put em dash and non-breaking-space text through real tmux drag-copy**

In `test("tmux drag-copy reaches the clipboard in tab and inline hosts", ...)`, replace both ASCII markers with literals that cover the reported corruption:

```ts
const tabMarker = "tab clipboard — marker\u00a0value";
```

and:

```ts
const inlineMarker = "inline clipboard — marker\u00a0value";
```

Keep the existing exact marker variables in the Firefox `fallbackWrites` and Chromium `readBrowserClipboard` assertions. `renderMarker` already converts the marker to shell octal bytes before writing it, so these literals cross the WebSocket and xterm parser as UTF-8 rather than depending on keyboard layout.

- [ ] **Step 2: Build the current frontend used by the full-stack server**

Run:

```bash
make frontend
```

Expected: PASS and refresh `internal/web/dist` for the e2e server without producing a tracked diff there.

- [ ] **Step 3: Run the affected real-tmux test in Chromium and Firefox**

Run:

```bash
cd frontend
../node_modules/.bin/vp exec -- playwright test \
  tests/e2e-full/00-tmux-browser-clipboard.spec.ts \
  --config=playwright-e2e.config.ts \
  --project=chromium \
  --project=firefox
```

Expected: PASS. Chromium reads the exact Unicode marker from the browser clipboard; Firefox observes the exact Unicode marker in the intercepted JSON fallback request. This is characterization at the frontend/server boundary and is expected to pass independently of the macOS `pbcopy` fix.

- [ ] **Step 4: Commit Task 2**

Invoke the repository-local `context-sync` skill with `--commit`, then the mandatory commit skill. Run:

```bash
git add frontend/tests/e2e-full/00-tmux-browser-clipboard.spec.ts
git commit -m "test: cover Unicode terminal clipboard fallback"
```

The commit body must explain that the browser test stops at the HTTP boundary because `pbcopy` locale behavior is macOS-specific and is covered at the Go command-runner boundary.

---

## Final Verification

- [ ] Run the full frontend unit suite:

```bash
cd frontend
../node_modules/.bin/vp test run --project unit
```

- [ ] Run frontend format, lint, type, and Svelte checks:

```bash
make frontend-check
```

- [ ] Rebuild the embedded frontend and rerun the affected full-stack suite after the final test edit:

```bash
make frontend
cd frontend
../node_modules/.bin/vp exec -- playwright test \
  tests/e2e-full/00-tmux-browser-clipboard.spec.ts \
  --config=playwright-e2e.config.ts \
  --project=chromium \
  --project=firefox
```

- [ ] Run the full short Go suite:

```bash
make test-short
```

- [ ] Confirm generated API artifacts and the working tree are clean:

```bash
make api-generate
git status --short
git diff --exit-code
```

Expected: all commands pass, `make api-generate` produces no diff, and the only commits after the approved design commits are the two implementation commits from Tasks 1 and 2.
