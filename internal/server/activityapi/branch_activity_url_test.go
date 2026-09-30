package activityapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.kenn.io/forge/internal/db"
)

func TestBitbucketBranchActivityURL(t *testing.T) {
	for _, tc := range []struct{ name, host, commit, before, after, want string }{
		{"cloud commit", "", "abc123", "", "", "https://bitbucket.org/PROJECT/widgets/commits/abc123"},
		{"cloud force push", "bitbucket.org", "", "old", "new", "https://bitbucket.org/PROJECT/widgets/commits/new"},
		{"data center commit", "code.example.test:8443", "abc123", "", "", "https://code.example.test:8443/projects/PROJECT/repos/widgets/commits/abc123"},
		{"data center force push without old head", "code.example.test", "", "", "new", "https://code.example.test/projects/PROJECT/repos/widgets/commits/new"},
		{"missing resulting commit", "bitbucket.org", "", "old", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, branchActivityURL(db.ActivityItem{Platform: "bitbucket", PlatformHost: tc.host, RepoOwner: "PROJECT", RepoName: "widgets", CommitSHA: tc.commit, BeforeSHA: tc.before, AfterSHA: tc.after}))
		})
	}
}
