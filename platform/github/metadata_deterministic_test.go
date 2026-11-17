package github

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMetadataEncodersAreDeterministic(t *testing.T) {
	cases := []struct {
		name   string
		want   string
		encode func() string
	}{
		{"commit order", `{"nested":{"z":1,"a":2},"other":true,"commit_order":3,"commit_order_key":4}`, func() string { return WithCommitOrderMetadata(`{"nested":{"z":1,"a":2},"other":true}`, 3, 4) }},
		{"visibility", `{"provider_hidden":true,"provider_hidden_reason":"outdated"}`, func() string {
			return NormalizeCommentVisibilityMetadata(CommentVisibility{Hidden: true, Reason: "outdated"})
		}},
		{"actor", `{"nested":{"z":1,"a":2},"other":true,"actor_account_type":"bot"}`, func() string { return WithActorAccountType(`{"nested":{"z":1,"a":2},"other":true}`, "Bot") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first := tc.encode()
			require.JSONEq(t, tc.want, first)
			for range 100 {
				require.Equal(t, first, tc.encode())
			}
		})
	}
}
