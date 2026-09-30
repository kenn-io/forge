package platform

import (
	"encoding/json/v2"
	"testing"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type keyedFixture struct {
	Provider string        `json:"provider"`
	Key      RepositoryKey `json:"-" repokey:"platform_repo_id,bitbucket_repository_uuid"`
	Owner    string        `json:"owner"`
}

func (r keyedFixture) MarshalJSON() ([]byte, error) {
	type plain keyedFixture
	return MarshalKeyedJSON(plain(r))
}

func (r *keyedFixture) UnmarshalJSON(data []byte) error {
	type plain keyedFixture
	return UnmarshalKeyedJSON(data, (*plain)(r))
}

// legacyFixture is the flat struct keyedFixture replaced; integer keys must
// keep its exact encoding.
type legacyFixture struct {
	Provider       string `json:"provider"`
	PlatformRepoID int64  `json:"platform_repo_id"`
	Owner          string `json:"owner"`
}

func TestKeyedJSONKeepsIntegerEncoding(t *testing.T) {
	encoded, err := json.Marshal(keyedFixture{Provider: "github", Key: RepositoryIDKey(42), Owner: "acme"})
	require.NoError(t, err)
	legacy, err := json.Marshal(legacyFixture{Provider: "github", PlatformRepoID: 42, Owner: "acme"})
	require.NoError(t, err)

	assert.JSONEq(t, string(legacy), string(encoded))
	assert.Equal(t, string(legacy), string(encoded), "member order must not change")
}

func TestKeyedJSONRoundTripsUUIDKey(t *testing.T) {
	want := keyedFixture{
		Provider: "bitbucket",
		Key:      RepositoryUUIDKey(uuid.MustParse("11111111-1111-4111-8111-111111111111")),
		Owner:    "team",
	}

	encoded, err := json.Marshal(want)
	require.NoError(t, err)
	var got keyedFixture
	require.NoError(t, json.Unmarshal(encoded, &got))

	assert.Equal(t, `{"provider":"bitbucket","platform_repo_id":0,"bitbucket_repository_uuid":"11111111-1111-4111-8111-111111111111","owner":"team"}`, string(encoded))
	assert.Equal(t, want, got)
}

func TestKeyedJSONRejectsBothKeyMembers(t *testing.T) {
	var got keyedFixture
	err := json.Unmarshal([]byte(`{"platform_repo_id":7,"bitbucket_repository_uuid":"11111111-1111-4111-8111-111111111111"}`), &got)

	assert.Error(t, err)
}
