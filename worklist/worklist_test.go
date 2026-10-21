package worklist_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/worklist"
)

func TestValidateChecksTheRepositoryKeyALinkMayCarry(t *testing.T) {
	list := func(ref worklist.ItemRef) worklist.List {
		ref.Type, ref.Provider, ref.Owner, ref.Name, ref.Number = "pr", "bitbucket", "acme", "widgets", 1
		return worklist.List{Title: "T", Items: []worklist.Item{{Item: ref}}}
	}
	tests := []struct {
		name    string
		ref     worklist.ItemRef
		wantErr bool
	}{
		{"route only", worklist.ItemRef{}, false},
		{"integer ID", worklist.ItemRef{PlatformRepoID: 1001}, false},
		{"braced UUID", worklist.ItemRef{BitbucketRepositoryUUID: "{0b0e9a43-6a51-4f3a-9a4f-0e7c2f3c4d5e}"}, false},
		{"negative ID", worklist.ItemRef{PlatformRepoID: -1}, true},
		{"malformed UUID", worklist.ItemRef{BitbucketRepositoryUUID: "not-a-uuid"}, true},
		{"both keys", worklist.ItemRef{PlatformRepoID: 1001, BitbucketRepositoryUUID: "0b0e9a43-6a51-4f3a-9a4f-0e7c2f3c4d5e"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := list(tt.ref).Validate()

			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			fieldErr, ok := errors.AsType[*worklist.FieldError](err)
			require.True(t, ok, "got %v", err)
			assert.Equal(t, "items[0].item", fieldErr.Field)
		})
	}
}
