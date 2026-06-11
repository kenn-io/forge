package gitclone

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/tokenauth"
)

type mutableTestTokenSource struct {
	token       string
	invalidated int
}

func (s *mutableTestTokenSource) Token(context.Context) (string, error) {
	return s.token, nil
}

func (s *mutableTestTokenSource) Invalidate() {
	s.invalidated++
	s.token = "second-token"
}

func (s *mutableTestTokenSource) Descriptor() tokenauth.Descriptor {
	return tokenauth.Descriptor{Key: tokenauth.Key{Platform: "test", Host: "github.com"}}
}

func TestGitNetworkedResolvesTokenSourceForEachCall(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dir := t.TempDir()
	capturePath := filepath.Join(dir, "credentials.txt")
	gitPath := filepath.Join(dir, "git")
	require.NoError(os.WriteFile(gitPath, []byte(`#!/bin/sh
set -eu
out="${MIDDLEMAN_TEST_GIT_CAPTURE:?}"
i=0
count="${GIT_CONFIG_COUNT:-0}"
while [ "$i" -lt "$count" ]; do
	eval "key=\${GIT_CONFIG_KEY_$i:-}"
	eval "value=\${GIT_CONFIG_VALUE_$i:-}"
	if [ "$key" = "credential.helper" ]; then
		"$value" get >> "$out"
		echo "---" >> "$out"
	fi
	i=$((i + 1))
done
`), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MIDDLEMAN_TEST_GIT_CAPTURE", capturePath)

	source := &mutableTestTokenSource{token: "first-token"}
	mgr := New(t.TempDir(), map[string]tokenauth.Source{"github.com": source})

	_, err := mgr.gitNetworked(t.Context(), "github.com", "", nil, "fetch")
	require.NoError(err)
	source.token = "second-token"
	_, err = mgr.gitNetworked(t.Context(), "github.com", "", nil, "fetch")
	require.NoError(err)

	data, err := os.ReadFile(capturePath)
	require.NoError(err)
	credentials := strings.Split(strings.TrimSpace(string(data)), "\n")

	assert.Equal([]string{
		"username=x-access-token",
		"password=first-token",
		"---",
		"username=x-access-token",
		"password=second-token",
		"---",
	}, credentials)
}

func TestGitNetworkedResolvesTokenFileSourceForEachCall(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dir := t.TempDir()
	capturePath := filepath.Join(dir, "credentials.txt")
	tokenPath := filepath.Join(dir, "token")
	gitPath := filepath.Join(dir, "git")
	require.NoError(os.WriteFile(gitPath, []byte(`#!/bin/sh
set -eu
out="${MIDDLEMAN_TEST_GIT_CAPTURE:?}"
i=0
count="${GIT_CONFIG_COUNT:-0}"
while [ "$i" -lt "$count" ]; do
	eval "key=\${GIT_CONFIG_KEY_$i:-}"
	eval "value=\${GIT_CONFIG_VALUE_$i:-}"
	if [ "$key" = "credential.helper" ]; then
		"$value" get >> "$out"
		echo "---" >> "$out"
	fi
	i=$((i + 1))
done
`), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MIDDLEMAN_TEST_GIT_CAPTURE", capturePath)

	require.NoError(os.WriteFile(tokenPath, []byte("first-token\n"), 0o600))
	source := tokenauth.NewManagedSource(tokenauth.Descriptor{
		Key: tokenauth.Key{Platform: "test", Host: "github.com"},
		Candidates: []tokenauth.Candidate{{
			Kind:     tokenauth.SourceKindFile,
			FilePath: tokenPath,
		}},
	}, tokenauth.Options{})
	mgr := New(t.TempDir(), map[string]tokenauth.Source{"github.com": source})

	_, err := mgr.gitNetworked(t.Context(), "github.com", "", nil, "fetch")
	require.NoError(err)
	require.NoError(os.WriteFile(tokenPath, []byte("second-token\n"), 0o600))
	_, err = mgr.gitNetworked(t.Context(), "github.com", "", nil, "fetch")
	require.NoError(err)

	data, err := os.ReadFile(capturePath)
	require.NoError(err)
	credentials := strings.Split(strings.TrimSpace(string(data)), "\n")

	assert.Equal([]string{
		"username=x-access-token",
		"password=first-token",
		"---",
		"username=x-access-token",
		"password=second-token",
		"---",
	}, credentials)
}

func TestGitRetriesAuthFailureAfterInvalidatingTokenSource(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dir := t.TempDir()
	capturePath := filepath.Join(dir, "credentials.txt")
	gitPath := filepath.Join(dir, "git")
	require.NoError(os.WriteFile(gitPath, []byte(`#!/bin/sh
set -eu
out="${MIDDLEMAN_TEST_GIT_CAPTURE:?}"
tmp="$out.current"
helper=""
i=0
count="${GIT_CONFIG_COUNT:-0}"
while [ "$i" -lt "$count" ]; do
	eval "key=\${GIT_CONFIG_KEY_$i:-}"
	eval "value=\${GIT_CONFIG_VALUE_$i:-}"
	if [ "$key" = "credential.helper" ]; then
		helper="$value"
	fi
	i=$((i + 1))
done
"$helper" get > "$tmp"
cat "$tmp" >> "$out"
echo "---" >> "$out"
password="$(sed -n 's/^password=//p' "$tmp")"
if [ "$password" = "first-token" ]; then
	echo "fatal: Authentication failed for 'https://github.com/acme/widgets.git/'" >&2
	exit 128
fi
`), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MIDDLEMAN_TEST_GIT_CAPTURE", capturePath)

	source := &mutableTestTokenSource{token: "first-token"}
	mgr := New(t.TempDir(), map[string]tokenauth.Source{"github.com": source})

	_, err := mgr.gitNetworked(t.Context(), "github.com", "", nil, "fetch", "origin")
	require.NoError(err)

	data, err := os.ReadFile(capturePath)
	require.NoError(err)
	credentials := strings.Split(strings.TrimSpace(string(data)), "\n")

	assert.Equal(1, source.invalidated)
	assert.Equal([]string{
		"username=x-access-token",
		"password=first-token",
		"---",
		"username=x-access-token",
		"password=second-token",
		"---",
	}, credentials)
}

func TestCloneBareRetriesAuthFailureAfterCleaningPartialClone(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dir := t.TempDir()
	capturePath := filepath.Join(dir, "credentials.txt")
	gitPath := filepath.Join(dir, "git")
	require.NoError(os.WriteFile(gitPath, []byte(`#!/bin/sh
set -eu
if [ "${1:-}" != "clone" ]; then
	exit 0
fi

out="${MIDDLEMAN_TEST_GIT_CAPTURE:?}"
dest="${4:?}"
if [ -e "$dest" ]; then
	echo "fatal: destination path '$dest' already exists and is not an empty directory." >&2
	exit 128
fi

helper=""
i=0
count="${GIT_CONFIG_COUNT:-0}"
while [ "$i" -lt "$count" ]; do
	eval "key=\${GIT_CONFIG_KEY_$i:-}"
	eval "value=\${GIT_CONFIG_VALUE_$i:-}"
	if [ "$key" = "credential.helper" ]; then
		helper="$value"
	fi
	i=$((i + 1))
done

tmp="$out.current"
"$helper" get > "$tmp"
cat "$tmp" >> "$out"
echo "---" >> "$out"
password="$(sed -n 's/^password=//p' "$tmp")"

mkdir -p "$dest"
if [ "$password" = "first-token" ]; then
	echo partial > "$dest/partial"
	echo "fatal: Authentication failed for 'https://github.com/acme/widgets.git/'" >&2
	exit 128
fi
echo complete > "$dest/complete"
`), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MIDDLEMAN_TEST_GIT_CAPTURE", capturePath)

	source := &mutableTestTokenSource{token: "first-token"}
	mgr := New(t.TempDir(), map[string]tokenauth.Source{"github.com": source})
	clonePath := filepath.Join(dir, "widgets.git")

	err := mgr.cloneBare(
		t.Context(), "github.com", clonePath,
		"https://github.com/acme/widgets.git",
	)
	require.NoError(err)

	data, err := os.ReadFile(capturePath)
	require.NoError(err)
	credentials := strings.Split(strings.TrimSpace(string(data)), "\n")

	assert.Equal(1, source.invalidated)
	assert.Equal([]string{
		"username=x-access-token",
		"password=first-token",
		"---",
		"username=x-access-token",
		"password=second-token",
		"---",
	}, credentials)
	assert.NoFileExists(filepath.Join(clonePath, "partial"))
	assert.FileExists(filepath.Join(clonePath, "complete"))
}

func TestGitNetworkedRedactsTokenFromGitStderr(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dir := t.TempDir()
	gitPath := filepath.Join(dir, "git")
	require.NoError(os.WriteFile(gitPath, []byte(`#!/bin/sh
set -eu
echo "fatal: Authentication failed for 'https://x-access-token:ghp_stderr_secret@github.com/acme/widgets.git/'" >&2
exit 128
`), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	source := &mutableTestTokenSource{token: "first-token"}
	mgr := New(t.TempDir(), map[string]tokenauth.Source{"github.com": source})

	_, err := mgr.gitNetworked(t.Context(), "github.com", "", nil, "fetch", "origin")
	require.Error(err)

	assert.NotContains(err.Error(), "ghp_stderr_secret")
	assert.NotContains(err.Error(), "x-access-token")
	assert.Contains(err.Error(), "[REDACTED]")
}

func TestWrapGitErrorPreservesContextCancellationIdentity(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "canceled", err: context.Canceled},
		{name: "deadline", err: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)

			err := wrapGitError(
				tc.err,
				[]byte("fatal: Authentication failed for 'https://x-access-token:ghp_context_secret@github.com/acme/widgets.git/'"),
			)

			require.ErrorIs(t, err, tc.err)
			assert.NotContains(err.Error(), "ghp_context_secret")
			assert.NotContains(err.Error(), "x-access-token")
			assert.Contains(err.Error(), "[REDACTED]")
		})
	}
}

func TestWrapGitErrorPreservesMissingTokenIdentity(t *testing.T) {
	assert := assert.New(t)

	err := wrapGitError(
		fmt.Errorf("resolve git token: %w", tokenauth.ErrMissingToken),
		[]byte("fatal: Authentication failed for 'https://x-access-token:ghp_missing_secret@github.com/acme/widgets.git/'"),
	)

	require.ErrorIs(t, err, tokenauth.ErrMissingToken)
	assert.NotContains(err.Error(), "ghp_missing_secret")
	assert.NotContains(err.Error(), "x-access-token")
	assert.Contains(err.Error(), "[REDACTED]")
}

// failingTokenSource never resolves a token, standing in for a token file that
// is briefly missing or empty mid-rotation. It counts how often the resolver
// was consulted so a test can assert local reads never touch it.
type failingTokenSource struct {
	calls int
}

func (s *failingTokenSource) Token(context.Context) (string, error) {
	s.calls++
	return "", tokenauth.ErrMissingToken
}

func (s *failingTokenSource) Invalidate() {}

func (s *failingTokenSource) Descriptor() tokenauth.Descriptor {
	return tokenauth.Descriptor{Key: tokenauth.Key{Platform: "test", Host: "github.com"}}
}

// TestLocalReadSkipsTokenSourceDuringRotation verifies a local read against an
// already-cloned repo (rev-parse) succeeds even when the host's token source
// cannot resolve a credential. Local git never contacts the remote, so it must
// not depend on a live token — otherwise a token file briefly missing during
// rotation would break commit and diff views.
func TestLocalReadSkipsTokenSourceDuringRotation(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dir := t.TempDir()
	gitPath := filepath.Join(dir, "git")
	require.NoError(os.WriteFile(gitPath, []byte(
		"#!/bin/sh\necho deadbeefdeadbeefdeadbeefdeadbeefdeadbeef\n",
	), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	source := &failingTokenSource{}
	clonesDir := t.TempDir()
	mgr := New(clonesDir, map[string]tokenauth.Source{"github.com": source})
	clonePath, err := mgr.ClonePath("github.com", "acme", "widgets")
	require.NoError(err)
	require.NoError(os.MkdirAll(clonePath, 0o755))

	sha, err := mgr.RevParse(t.Context(), "github.com", "acme", "widgets", "HEAD")
	require.NoError(err)
	assert.Equal("deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", sha)
	assert.Zero(source.calls, "local read must not resolve the token source")
}
