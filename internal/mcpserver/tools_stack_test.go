package mcpserver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetStackContextTreatsTypedNotStackedAsAbsent(t *testing.T) {
	backend := &fakeBackend{getPullStackFn: func(context.Context, ItemIdentity) (Stack, error) {
		return Stack{}, &Error{Kind: "not_found", Code: "notFound", Message: "PR is not part of a stack"}
	}}
	s := newMCPTestServer(t, backend)

	out, err := s.getStackContext(t.Context(), getStackContextInput{
		Item: itemRefInput{Type: "pr", Provider: "github", PlatformRepoID: 1001, Owner: "acme", Name: "widget", Number: 42},
	})

	require.NoError(t, err)
	assert.False(t, out.Present)

	_, err = s.getStackContext(t.Context(), getStackContextInput{
		Item: itemRefInput{Type: "issue", Provider: "github", PlatformRepoID: 1001, Owner: "acme", Name: "widget", Number: 7},
	})
	assertBackendErrorKind(t, err, "invalid_request")
}

func TestGetStackContextPropagatesMissingPull(t *testing.T) {
	backend := &fakeBackend{getPullStackFn: func(context.Context, ItemIdentity) (Stack, error) {
		return Stack{}, &Error{Kind: "not_found", Code: "pullNotFound", Message: "pull request not found"}
	}}
	s := newMCPTestServer(t, backend)

	_, err := s.getStackContext(t.Context(), getStackContextInput{
		Item: itemRefInput{Type: "pr", Provider: "github", PlatformRepoID: 1001, Owner: "acme", Name: "widget", Number: 99},
	})

	var backendErr *Error
	require.ErrorAs(t, err, &backendErr)
	assert.Equal(t, "pullNotFound", backendErr.Code)
}

func TestGetStackContextSortsMembers(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	backend := &fakeBackend{
		getPullStackFn: func(context.Context, ItemIdentity) (Stack, error) {
			return Stack{Health: "blocked", Members: []StackMember{
				{Number: 43, Title: "Tip", State: "open", Position: 3},
				{Number: 41, Title: "Base", State: "open", Position: 1},
				{Number: 42, Title: "Middle", State: "open", Position: 2, IsDraft: true},
			}}, nil
		},
	}
	s := newMCPTestServer(t, backend)

	out, err := s.getStackContext(t.Context(), getStackContextInput{
		Item: itemRefInput{
			Type: "pr", Provider: "github", PlatformRepoID: 1001, PlatformHost: "github.com",
			Owner: "acme", Name: "widget", Number: 42,
		},
	})

	require.NoError(err)
	require.Len(out.Members, 3)
	assert.Equal(41, out.Members[0].Number)
	assert.True(out.Members[1].IsRequested)
	assert.True(out.Members[1].IsDraft)
}
