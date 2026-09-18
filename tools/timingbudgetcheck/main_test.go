package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testDiagnosticMessage = "wait on the in-process signal or synctest, or add a reviewed allowance in tools/timingbudgetcheck"

type expectedDiagnostic struct {
	path      string
	source    string
	needle    string
	assertion string
	budget    time.Duration
}

func TestRunReportsSubSecondLiteralBudgets(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	root := t.TempDir()
	source := `package fixture

import (
	a "github.com/stretchr/testify/assert"
	r "github.com/stretchr/testify/require"
	stdtime "time"
)

var packageBudget = a.Never(nil, func() bool { return false }, 0, stdtime.Nanosecond)

func checks(t a.TestingT, param *a.Assertions) {
	a.Eventually(t, func() bool { return true }, 999*stdtime.Millisecond, stdtime.Millisecond)
	a.EventuallyWithT(t, func(*a.CollectT) {}, stdtime.Second/2, stdtime.Millisecond)
	a.Eventuallyf(t, func() bool { return true }, 500_000_000, stdtime.Millisecond, "message")
	a.EventuallyWithTf(t, func(*a.CollectT) {}, 0, stdtime.Millisecond, "message")
	a.Never(t, func() bool { return false }, -50*stdtime.Millisecond, stdtime.Millisecond)
	a.Neverf(t, func() bool { return false }, (stdtime.Second-900*stdtime.Millisecond)/2, stdtime.Millisecond, "message")

	r.Eventually(t, func() bool { return true }, stdtime.Duration(50)*stdtime.Millisecond, stdtime.Millisecond)
	r.EventuallyWithT(t, func(*a.CollectT) {}, 49999*stdtime.Microsecond+1000*stdtime.Nanosecond, stdtime.Millisecond)
	r.Eventuallyf(t, func() bool { return true }, 999*stdtime.Millisecond, stdtime.Millisecond, "message")
	r.EventuallyWithTf(t, func(*a.CollectT) {}, stdtime.Second/2, stdtime.Millisecond, "message")
	r.Never(t, func() bool { return false }, 0, stdtime.Millisecond)
	r.Neverf(t, func() bool { return false }, -50*stdtime.Millisecond, stdtime.Millisecond, "message")

	assert := a.New(t)
	require := r.New(t)
	assert.Eventually(func() bool { return true }, stdtime.Millisecond, stdtime.Millisecond)
	assert.EventuallyWithT(func(*a.CollectT) {}, 2*stdtime.Millisecond, stdtime.Millisecond)
	assert.Eventuallyf(func() bool { return true }, 3*stdtime.Millisecond, stdtime.Millisecond, "message")
	assert.EventuallyWithTf(func(*a.CollectT) {}, 4*stdtime.Millisecond, stdtime.Millisecond, "message")
	assert.Never(func() bool { return false }, 5*stdtime.Millisecond, stdtime.Millisecond)
	assert.Neverf(func() bool { return false }, 6*stdtime.Millisecond, stdtime.Millisecond, "message")
	require.Eventually(func() bool { return true }, 7*stdtime.Millisecond, stdtime.Millisecond)
	require.EventuallyWithT(func(*a.CollectT) {}, 8*stdtime.Millisecond, stdtime.Millisecond)
	require.Eventuallyf(func() bool { return true }, 9*stdtime.Millisecond, stdtime.Millisecond, "message")
	require.EventuallyWithTf(func(*a.CollectT) {}, 10*stdtime.Millisecond, stdtime.Millisecond, "message")
	require.Never(func() bool { return false }, 11*stdtime.Millisecond, stdtime.Millisecond)
	require.Neverf(func() bool { return false }, 12*stdtime.Millisecond, stdtime.Millisecond, "message")
	param.Never(func() bool { return false }, 13*stdtime.Millisecond, stdtime.Millisecond)
	(*a.Assertions).Never(a.New(t), func() bool { return false }, 14*stdtime.Millisecond, stdtime.Millisecond)
	((a.Never))(t, func() bool { return false }, 15*stdtime.Millisecond, stdtime.Millisecond)
}
`
	snapshot := writeFixture(t, root, map[string]string{
		"go.mod":    "module fixture\n\ngo 1.27\n",
		"a_test.go": source,
	})

	code, stdout, stderr := runFixture(root)
	want := ""
	for _, item := range []expectedDiagnostic{
		{path: "a_test.go", source: source, needle: "a.Never(nil", assertion: "github.com/stretchr/testify/assert.Never", budget: 0},
		{path: "a_test.go", source: source, needle: "a.Eventually(t", assertion: "github.com/stretchr/testify/assert.Eventually", budget: 999 * time.Millisecond},
		{path: "a_test.go", source: source, needle: "a.EventuallyWithT(t", assertion: "github.com/stretchr/testify/assert.EventuallyWithT", budget: 500 * time.Millisecond},
		{path: "a_test.go", source: source, needle: "a.Eventuallyf(t", assertion: "github.com/stretchr/testify/assert.Eventuallyf", budget: 500 * time.Millisecond},
		{path: "a_test.go", source: source, needle: "a.EventuallyWithTf(t", assertion: "github.com/stretchr/testify/assert.EventuallyWithTf", budget: 0},
		{path: "a_test.go", source: source, needle: "a.Never(t", assertion: "github.com/stretchr/testify/assert.Never", budget: -50 * time.Millisecond},
		{path: "a_test.go", source: source, needle: "a.Neverf(t", assertion: "github.com/stretchr/testify/assert.Neverf", budget: 50 * time.Millisecond},
		{path: "a_test.go", source: source, needle: "r.Eventually(t", assertion: "github.com/stretchr/testify/require.Eventually", budget: 50 * time.Millisecond},
		{path: "a_test.go", source: source, needle: "r.EventuallyWithT(t", assertion: "github.com/stretchr/testify/require.EventuallyWithT", budget: 50 * time.Millisecond},
		{path: "a_test.go", source: source, needle: "r.Eventuallyf(t", assertion: "github.com/stretchr/testify/require.Eventuallyf", budget: 999 * time.Millisecond},
		{path: "a_test.go", source: source, needle: "r.EventuallyWithTf(t", assertion: "github.com/stretchr/testify/require.EventuallyWithTf", budget: 500 * time.Millisecond},
		{path: "a_test.go", source: source, needle: "r.Never(t", assertion: "github.com/stretchr/testify/require.Never", budget: 0},
		{path: "a_test.go", source: source, needle: "r.Neverf(t", assertion: "github.com/stretchr/testify/require.Neverf", budget: -50 * time.Millisecond},
		{path: "a_test.go", source: source, needle: "assert.Eventually(", assertion: "github.com/stretchr/testify/assert.Eventually", budget: time.Millisecond},
		{path: "a_test.go", source: source, needle: "assert.EventuallyWithT(", assertion: "github.com/stretchr/testify/assert.EventuallyWithT", budget: 2 * time.Millisecond},
		{path: "a_test.go", source: source, needle: "assert.Eventuallyf(", assertion: "github.com/stretchr/testify/assert.Eventuallyf", budget: 3 * time.Millisecond},
		{path: "a_test.go", source: source, needle: "assert.EventuallyWithTf(", assertion: "github.com/stretchr/testify/assert.EventuallyWithTf", budget: 4 * time.Millisecond},
		{path: "a_test.go", source: source, needle: "assert.Never(", assertion: "github.com/stretchr/testify/assert.Never", budget: 5 * time.Millisecond},
		{path: "a_test.go", source: source, needle: "assert.Neverf(", assertion: "github.com/stretchr/testify/assert.Neverf", budget: 6 * time.Millisecond},
		{path: "a_test.go", source: source, needle: "require.Eventually(", assertion: "github.com/stretchr/testify/require.Eventually", budget: 7 * time.Millisecond},
		{path: "a_test.go", source: source, needle: "require.EventuallyWithT(", assertion: "github.com/stretchr/testify/require.EventuallyWithT", budget: 8 * time.Millisecond},
		{path: "a_test.go", source: source, needle: "require.Eventuallyf(", assertion: "github.com/stretchr/testify/require.Eventuallyf", budget: 9 * time.Millisecond},
		{path: "a_test.go", source: source, needle: "require.EventuallyWithTf(", assertion: "github.com/stretchr/testify/require.EventuallyWithTf", budget: 10 * time.Millisecond},
		{path: "a_test.go", source: source, needle: "require.Never(", assertion: "github.com/stretchr/testify/require.Never", budget: 11 * time.Millisecond},
		{path: "a_test.go", source: source, needle: "require.Neverf(", assertion: "github.com/stretchr/testify/require.Neverf", budget: 12 * time.Millisecond},
		{path: "a_test.go", source: source, needle: "param.Never(", assertion: "github.com/stretchr/testify/assert.Never", budget: 13 * time.Millisecond},
		{path: "a_test.go", source: source, needle: "(*a.Assertions).Never(", assertion: "github.com/stretchr/testify/assert.Never", budget: 14 * time.Millisecond},
		{path: "a_test.go", source: source, needle: "((a.Never))(", assertion: "github.com/stretchr/testify/assert.Never", budget: 15 * time.Millisecond},
	} {
		want += diagnosticFor(item)
	}

	require.Equal(1, code)
	assert.Equal(want, stdout)
	assert.Empty(stderr)
	assertFilesUnchanged(t, root, snapshot)
}

func TestRunAcceptsBudgetsOutsideTheRule(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	root := t.TempDir()
	source := `package fixture

import (
	a "github.com/stretchr/testify/assert"
	stdtime "time"
)

const settle = 50 * stdtime.Millisecond
var namedWait = settle

func waitBudget() stdtime.Duration { return settle }

type localAssertions struct{}

func (localAssertions) Never(condition bool, waitFor stdtime.Duration, tick stdtime.Duration) {}

func checks(t a.TestingT) {
	a.Eventually(t, func() bool { return true }, stdtime.Second, stdtime.Millisecond)
	a.Eventually(t, func() bool { return true }, 1000*stdtime.Millisecond, stdtime.Millisecond)
	a.Eventually(t, func() bool { return true }, 2*stdtime.Second, stdtime.Millisecond)
	a.Eventually(t, func() bool { return true }, stdtime.Minute, stdtime.Millisecond)
	a.Eventually(t, func() bool { return true }, settle, stdtime.Millisecond)
	a.Eventually(t, func() bool { return true }, namedWait, stdtime.Millisecond)
	a.Eventually(t, func() bool { return true }, waitBudget(), stdtime.Millisecond)
	a.Eventually(t, func() bool { return true }, stdtime.Second, 50*stdtime.Millisecond)
	stdtime.Sleep(stdtime.Millisecond)

	var local localAssertions
	local.Never(false, 50*stdtime.Millisecond, stdtime.Millisecond)
	(*a.Assertions).Never(a.New(t), func() bool { return false }, stdtime.Second, stdtime.Millisecond)
	a.Never(t, func() bool { return false }, 0.5*stdtime.Second, stdtime.Millisecond)
	a.Never(t, func() bool { return false }, stdtime.Second/0, stdtime.Millisecond)
	a.Never(t, func() bool { return false }, 1<<100, stdtime.Millisecond)
}
`
	otherSource := `package fixture

import (
	assert "example.org/assert"
	time "example.org/clock"
)

func checks() {
	assert.Never(nil, func() bool { return false }, 50*time.Millisecond, time.Millisecond)
}
`
	localVariableSource := `package fixture

import a "github.com/stretchr/testify/assert"

type localAssertions struct{}
func (localAssertions) Never(bool, int, int) {}

func checks(t a.TestingT) {
	assert := localAssertions{}
	assert.Never(false, 50, 1)
}
`
	snapshot := writeFixture(t, root, map[string]string{
		"go.mod":                 "module fixture\n\ngo 1.27\n",
		"outside_test.go":        source,
		"unrelated_test.go":      otherSource,
		"local_variable_test.go": localVariableSource,
	})

	code, stdout, stderr := runFixture(root)
	require.Equal(0, code)
	assert.Empty(stdout)
	assert.Empty(stderr)
	assertFilesUnchanged(t, root, snapshot)
}

func TestRunSelectsTestFiles(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	parent := t.TempDir()
	root := filepath.Join(parent, "_root")
	files := map[string]string{
		"go.mod":                       "module fixture\n\ngo 1.27\n",
		"a_test.go":                    simpleBudgetSource("TestA", "50*time.Millisecond"),
		"z_test.go":                    simpleBudgetSource("TestZ", "40*time.Millisecond"),
		"darwin_test.go":               "//go:build darwin\n\n" + simpleBudgetSource("TestDarwin", "30*time.Millisecond"),
		"_windows_test.go":             "//go:build windows\n\n" + simpleBudgetSource("TestWindows", "20*time.Millisecond"),
		"nested/b_test.go":             simpleBudgetSource("TestNested", "time.Second"),
		"plain.go":                     simpleBudgetSource("TestPlain", "1*time.Millisecond"),
		"vendor/skip_test.go":          simpleBudgetSource("TestVendor", "1*time.Millisecond"),
		"node_modules/skip_test.go":    simpleBudgetSource("TestNodeModules", "1*time.Millisecond"),
		"testdata/skip_test.go":        simpleBudgetSource("TestTestdata", "1*time.Millisecond"),
		".hidden/skip_test.go":         simpleBudgetSource("TestHidden", "1*time.Millisecond"),
		"_fixtures/skip_test.go":       simpleBudgetSource("TestFixtures", "1*time.Millisecond"),
		"nested/testdata/skip_test.go": simpleBudgetSource("TestNestedTestdata", "1*time.Millisecond"),
		"internal/github/sync_test.go": allowanceSource("require", "TestTerminalStatusPublicationKeepsRunSlotUntilOrdered", "100*time.Millisecond"),
	}
	snapshot := writeFixture(t, root, files)

	code, stdout, stderr := runFixture(root)
	want := ""
	for _, item := range []expectedDiagnostic{
		{path: "_windows_test.go", source: files["_windows_test.go"], needle: "assert.Never", assertion: "github.com/stretchr/testify/assert.Never", budget: 20 * time.Millisecond},
		{path: "a_test.go", source: files["a_test.go"], needle: "assert.Never", assertion: "github.com/stretchr/testify/assert.Never", budget: 50 * time.Millisecond},
		{path: "darwin_test.go", source: files["darwin_test.go"], needle: "assert.Never", assertion: "github.com/stretchr/testify/assert.Never", budget: 30 * time.Millisecond},
		{path: "z_test.go", source: files["z_test.go"], needle: "assert.Never", assertion: "github.com/stretchr/testify/assert.Never", budget: 40 * time.Millisecond},
	} {
		want += diagnosticFor(item)
	}
	require.Equal(1, code)
	assert.Equal(want, stdout)
	assert.Empty(stderr)
	assertFilesUnchanged(t, root, snapshot)

	code, stdout, stderr = runFixture(filepath.Join(root, "internal", "github"))
	require.Equal(0, code)
	assert.Empty(stdout)
	assert.Empty(stderr)
	assertFilesUnchanged(t, root, snapshot)
}

func TestRunAllowedBudgets(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	expected := map[budgetKey]budgetAllowance{
		{path: "internal/github/sync_test.go", function: "TestTerminalStatusPublicationKeepsRunSlotUntilOrdered", assertion: "github.com/stretchr/testify/require.Never", budget: 100 * time.Millisecond}: {
			count: 1, reason: "Real syncer over an openTestDB SQLite fixture. The test holds syncer.statusMu across terminal publication, and mutex waits are not durably blocking in a synctest bubble.",
		},
		{path: "internal/server/api_test.go", function: "TestAPIEnqueuePRSyncQueuesOneRerun", assertion: "github.com/stretchr/testify/assert.Never", budget: 100 * time.Millisecond}: {
			count: 1, reason: "Rerun enqueued through the generated HTTP client against a SQLite-backed server. Retained by #1110.",
		},
		{path: "internal/server/federation_events_test.go", function: "TestNodeStreamsHubEventsWithNodeLocalCursorIDs", assertion: "github.com/stretchr/testify/assert.Never", budget: 50 * time.Millisecond}: {
			count: 1, reason: "Hub events over an httptest TLS federation stream to a disabled spoke. Retained by #1112.",
		},
		{path: "internal/server/repobrowserapi/handler_test.go", function: "TestRepoBrowserStartupRefreshHonorsDisabledBackgroundMonitors", assertion: "github.com/stretchr/testify/require.Never", budget: 250 * time.Millisecond}: {
			count: 1, reason: "Real Git clone ref resolution after an upstream push, with background monitors disabled.",
		},
		{path: "internal/server/runtime_launch_rollback_test.go", function: "TestCommandSameKeyPersistenceOwnership", assertion: "github.com/stretchr/testify/require.Never", budget: 150 * time.Millisecond}: {
			count: 1, reason: "SQLite writer-pool WaitCount while a fake-tmux subprocess launch on a PTY holds rollback authority.",
		},
		{path: "internal/server/runtime_launch_rollback_test.go", function: "TestProjectWorktreeShellPersistenceOwnership", assertion: "github.com/stretchr/testify/require.Never", budget: 150 * time.Millisecond}: {
			count: 1, reason: "Same owner, for the project worktree shell route.",
		},
		{path: "internal/workspace/localruntime/command_session_test.go", function: "TestEnsureCommandSessionAndPersistSerializesSameKeyOwnership", assertion: "github.com/stretchr/testify/require.Never", budget: 100 * time.Millisecond}: {
			count: 1, reason: "Fake-tmux subprocess on a PTY. The follower must not reach persistence while the creator holds the keyed start lock.",
		},
	}
	require.Equal(expected, allowedBudgets)

	root := t.TempDir()
	files := map[string]string{"go.mod": "module fixture\n\ngo 1.27\n"}
	for key := range allowedBudgets {
		assertion := "assert"
		if strings.Contains(key.assertion, "/require.") {
			assertion = "require"
		}
		files[key.path] = allowanceSource(assertion, key.function, durationExpression(key.budget))
	}
	snapshot := writeFixture(t, root, files)
	for range 2 {
		code, stdout, stderr := runFixture(root)
		require.Equal(0, code)
		assert.Empty(stdout)
		assert.Empty(stderr)
	}
	assertFilesUnchanged(t, root, snapshot)

	path := filepath.Join(root, "internal", "github", "sync_test.go")
	extra := strings.Replace(
		files["internal/github/sync_test.go"],
		"\trequire.Never(t, func() bool { return false }, 100*time.Millisecond, time.Millisecond)\n}",
		"\trequire.Never(t, func() bool { return false }, 100*time.Millisecond, time.Millisecond)\n\trequire.Never(t, func() bool { return true }, 100*time.Millisecond, time.Millisecond)\n}",
		1,
	)
	require.NoError(os.WriteFile(path, []byte(extra), 0o600))
	code, stdout, stderr := runFixture(root)
	want := diagnosticForIndex(expectedDiagnostic{
		path:      "internal/github/sync_test.go",
		source:    extra,
		assertion: "github.com/stretchr/testify/require.Never",
		budget:    100 * time.Millisecond,
	}, strings.LastIndex(extra, "require.Never"))
	require.Equal(1, code)
	assert.Equal(want, stdout)
	assert.Empty(stderr)

	replacement := strings.Replace(
		files["internal/github/sync_test.go"],
		"return false",
		"return true",
		1,
	)
	require.NoError(os.WriteFile(path, []byte(replacement), 0o600))
	code, stdout, stderr = runFixture(root)
	require.Equal(0, code)
	assert.Empty(stdout)
	assert.Empty(stderr)

	variants := []struct {
		name      string
		path      string
		src       string
		needle    string
		assertion string
		budget    time.Duration
	}{
		{name: "changed duration", path: "internal/github/sync_test.go", src: allowanceSource("require", "TestTerminalStatusPublicationKeepsRunSlotUntilOrdered", "101*time.Millisecond"), needle: "require.Never", assertion: "github.com/stretchr/testify/require.Never", budget: 101 * time.Millisecond},
		{name: "changed function", path: "internal/github/sync_test.go", src: allowanceSource("require", "TestOther", "100*time.Millisecond"), needle: "require.Never", assertion: "github.com/stretchr/testify/require.Never", budget: 100 * time.Millisecond},
		{name: "changed file", path: "internal/github/other_test.go", src: allowanceSource("require", "TestTerminalStatusPublicationKeepsRunSlotUntilOrdered", "100*time.Millisecond"), needle: "require.Never", assertion: "github.com/stretchr/testify/require.Never", budget: 100 * time.Millisecond},
		{name: "changed assertion", path: "internal/github/sync_test.go", src: allowanceSource("assert", "TestTerminalStatusPublicationKeepsRunSlotUntilOrdered", "100*time.Millisecond"), needle: "assert.Never", assertion: "github.com/stretchr/testify/assert.Never", budget: 100 * time.Millisecond},
	}
	for _, variant := range variants {
		_ = os.Remove(filepath.Join(root, "internal", "github", "other_test.go"))
		for filePath, content := range files {
			if filePath == "go.mod" {
				continue
			}
			require.NoError(os.WriteFile(filepath.Join(root, filepath.FromSlash(filePath)), []byte(content), 0o600))
		}
		require.NoError(os.WriteFile(filepath.Join(root, filepath.FromSlash(variant.path)), []byte(variant.src), 0o600))
		code, stdout, stderr = runFixture(root)
		want := diagnosticFor(expectedDiagnostic{
			path:      variant.path,
			source:    variant.src,
			needle:    variant.needle,
			assertion: variant.assertion,
			budget:    variant.budget,
		})
		require.Equal(1, code, variant.name)
		assert.Equal(want, stdout, variant.name)
		assert.Empty(stderr, variant.name)
	}

	for filePath, content := range files {
		if filePath == "go.mod" {
			continue
		}
		require.NoError(os.WriteFile(filepath.Join(root, filepath.FromSlash(filePath)), []byte(content), 0o600))
	}
	_ = os.Remove(filepath.Join(root, "internal", "github", "other_test.go"))
	require.NoError(os.Remove(filepath.Join(root, "internal", "github", "sync_test.go")))
	code, stdout, stderr = runFixture(root)
	require.Equal(0, code)
	assert.Empty(stdout)
	assert.Empty(stderr)
}

func TestRunErrors(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	root := t.TempDir()
	valid := simpleBudgetSource("TestValid", "50*time.Millisecond")
	broken := "package fixture\n\nfunc broken( {\n"
	snapshot := writeFixture(t, root, map[string]string{
		"go.mod":    "module fixture\n\ngo 1.27\n",
		"a_test.go": valid,
		"z_test.go": broken,
	})
	code, stdout, stderr := runFixture(root)
	require.Equal(1, code)
	assert.Empty(stdout)
	assert.Contains(stderr, "timingbudgetcheck:")
	assert.Contains(stderr, filepath.Join(root, "z_test.go"))
	assertFilesUnchanged(t, root, snapshot)

	missing := filepath.Join(t.TempDir(), "missing")
	code, stdout, stderr = runFixture(missing)
	require.Equal(1, code)
	assert.Empty(stdout)
	assert.Contains(stderr, "timingbudgetcheck:")
	assert.Contains(stderr, missing)

	fileRoot := filepath.Join(t.TempDir(), "file")
	require.NoError(os.WriteFile(fileRoot, []byte("content"), 0o600))
	code, stdout, stderr = runFixture(fileRoot)
	require.Equal(1, code)
	assert.Empty(stdout)
	assert.Equal(fmt.Sprintf("timingbudgetcheck: %s is not a directory\n", fileRoot), stderr)

	code, stdout, stderr = runFixtureWithArgs([]string{"one", "two"})
	require.Equal(1, code)
	assert.Empty(stdout)
	assert.Equal("usage: timingbudgetcheck [directory]\n", stderr)

	mixedRoot := t.TempDir()
	mixedSnapshot := writeFixture(t, mixedRoot, map[string]string{
		"go.mod":    "module fixture\n\ngo 1.27\n",
		"a_test.go": valid,
		"z_test.go": broken,
	})
	code, stdout, stderr = runFixture(mixedRoot)
	require.Equal(1, code)
	assert.Empty(stdout)
	assert.Contains(stderr, filepath.Join(mixedRoot, "z_test.go"))
	assertFilesUnchanged(t, mixedRoot, mixedSnapshot)
}

func simpleBudgetSource(function, budget string) string {
	return fmt.Sprintf(`package fixture

import (
	"time"
	"github.com/stretchr/testify/assert"
)

func %s(t assert.TestingT) {
	assert.Never(t, func() bool { return false }, %s, time.Millisecond)
}
`, function, budget)
}

func allowanceSource(assertion, function, budget string) string {
	importPath := "github.com/stretchr/testify/" + assertion
	return fmt.Sprintf(`package fixture

import (
	"time"
	"%s"
)

func %s(t %s.TestingT) {
	%s.Never(t, func() bool { return false }, %s, time.Millisecond)
}
`, importPath, function, assertion, assertion, budget)
}

func durationExpression(value time.Duration) string {
	switch value {
	case 50 * time.Millisecond:
		return "50*time.Millisecond"
	case 100 * time.Millisecond:
		return "100*time.Millisecond"
	case 150 * time.Millisecond:
		return "150*time.Millisecond"
	case 250 * time.Millisecond:
		return "250*time.Millisecond"
	default:
		panic("unexpected allowance duration")
	}
}

func writeFixture(t *testing.T, root string, files map[string]string) map[string][]byte {
	t.Helper()
	require := require.New(t)
	snapshot := make(map[string][]byte, len(files))
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		require.NoError(os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(os.WriteFile(path, []byte(content), 0o600))
		snapshot[name] = []byte(content)
	}
	return snapshot
}

func runFixture(root string) (int, string, string) {
	return runFixtureWithArgs([]string{root})
}

func runFixtureWithArgs(args []string) (int, string, string) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func assertFilesUnchanged(t *testing.T, root string, snapshot map[string][]byte) {
	t.Helper()
	assert := assert.New(t)
	require := require.New(t)
	for name, want := range snapshot {
		got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		require.NoError(err, name)
		assert.Equal(want, got, name)
	}
}

func diagnosticFor(item expectedDiagnostic) string {
	index := strings.Index(item.source, item.needle)
	if index < 0 {
		panic("diagnostic marker not found: " + item.needle)
	}
	return diagnosticForIndex(item, index)
}

func diagnosticForIndex(item expectedDiagnostic, index int) string {
	if index < 0 {
		panic("diagnostic marker not found")
	}
	line := 1 + strings.Count(item.source[:index], "\n")
	column := index - strings.LastIndex(item.source[:index], "\n")
	return fmt.Sprintf(
		"%s:%d:%d: %s budget %s is below 1s; %s\n",
		item.path,
		line,
		column,
		item.assertion,
		item.budget.String(),
		testDiagnosticMessage,
	)
}
