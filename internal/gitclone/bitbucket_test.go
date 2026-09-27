//go:build integration

package gitclone

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/platform"
)

func TestIntegrationBitbucketFetchHeadFromPrivateRefOrFork(t *testing.T) {
	for _, privateRef := range []string{"matching", "missing", "stale"} {
		t.Run(privateRef, func(t *testing.T) {
			root := t.TempDir()
			remote, work := setupTestRepo(t)
			base := gitSHA(t, work, "HEAD")
			fork := filepath.Join(root, "contributor", "widgets.git")
			require.NoError(t, os.MkdirAll(filepath.Dir(fork), 0o755))
			run(t, root, "git", "clone", "--bare", remote, fork)
			run(t, work, "git", "checkout", "-b", "feature")
			require.NoError(t, os.WriteFile(filepath.Join(work, "fork.txt"), []byte("fork content\n"), 0o644))
			run(t, work, "git", "add", "fork.txt")
			run(t, work, "git", "commit", "-m", "fork change")
			head := gitSHA(t, work, "HEAD")
			run(t, work, "git", "push", fork, "feature")
			run(t, fork, "git", "update-server-info")
			server := httptest.NewServer(http.FileServer(http.Dir(root)))
			t.Cleanup(server.Close)
			host := strings.TrimPrefix(server.URL, "http://")
			mgr := New(t.TempDir(), nil)
			mgr.SetAllowInsecureHTTP("bitbucket", host, true)
			require.NoError(t, mgr.EnsureClone(t.Context(), "bitbucket", host, "PROJECT", "widgets", remote))
			_, err := mgr.MergeBase(t.Context(), "bitbucket", host, "PROJECT", "widgets", base, head)
			require.Error(t, err)
			switch privateRef {
			case "matching":
				run(t, work, "git", "push", remote, head+":refs/pull-requests/7/from")
			case "stale":
				run(t, work, "git", "push", remote, base+":refs/pull-requests/7/from")
			}
			sourceURL := server.URL + "/contributor/widgets.git"
			if privateRef == "matching" {
				sourceURL = ""
			} // No source branch needed when the private ref matches.
			require.NoError(t, mgr.FetchBitbucketMergeRequestHead(t.Context(), host, "PROJECT", "widgets", 7, sourceURL, "feature", head))
			mergeBase, err := mgr.MergeBase(t.Context(), "bitbucket", host, "PROJECT", "widgets", base, head)
			require.NoError(t, err)
			assert.Equal(t, base, mergeBase)
			if privateRef != "matching" {
				err = mgr.FetchBitbucketMergeRequestHead(t.Context(), host, "PROJECT", "widgets", 8, sourceURL, "feature", base)
				require.ErrorIs(t, err, platform.ErrStaleState)
			}
		})
	}
}
