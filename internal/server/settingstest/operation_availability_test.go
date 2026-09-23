package settingstest

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/server/httpapi"
)

func TestRepoOperationsWireShape(t *testing.T) {
	// The set of operation field names on httpapi.RepoOperations is a wire
	// contract. Renaming a json tag here breaks any frontend pinned
	// to an older schema, so the test enumerates the full set as a
	// guard against accidental renames.
	require := require.New(t)
	fields := reflect.VisibleFields(reflect.TypeFor[httpapi.RepoOperations]())
	tags := make([]string, 0, len(fields))
	for _, f := range fields {
		tag := f.Tag.Get("json")
		require.NotEmpty(tag, "field %s missing json tag", f.Name)
		tags = append(tags, tag)
	}
	require.Equal([]string{
		"merge_pr",
		"close_pr",
		"reopen_pr",
		"mark_ready_for_review",
		"mark_draft",
		"submit_review",
		"review_draft",
		"add_comment",
		"edit_comment",
		"delete_comment",
		"add_label",
		"remove_label",
		"set_assignees",
		"set_reviewers",
		"create_issue",
		"close_issue",
		"reopen_issue",
		"approve_workflow",
		"dispatch_workflow",
		"update_content",
		"reply_review_thread",
		"resolve_review_thread",
		"apply_review_suggestion",
	}, tags)
}
