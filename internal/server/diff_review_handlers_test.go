package server

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDBReviewLineRangeRejectsMalformedMultilineRanges(t *testing.T) {
	validLine := 10
	cases := []struct {
		name  string
		patch func(*diffReviewLineRange)
	}{
		{
			name: "non-positive start line",
			patch: func(input *diffReviewLineRange) {
				startLine := 0
				input.StartSide = "right"
				input.StartLine = &startLine
			},
		},
		{
			name: "start line without start side",
			patch: func(input *diffReviewLineRange) {
				input.StartLine = &validLine
			},
		},
		{
			name: "start side without start line",
			patch: func(input *diffReviewLineRange) {
				input.StartSide = "right"
			},
		},
		{
			name: "start line after end line",
			patch: func(input *diffReviewLineRange) {
				startLine := 11
				input.StartSide = "right"
				input.StartLine = &startLine
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			input := diffReviewLineRange{
				Path:        "src/main.go",
				Side:        "right",
				Line:        10,
				NewLine:     &validLine,
				LineType:    "add",
				DiffHeadSHA: "head-sha",
			}
			tc.patch(&input)

			_, err := dbReviewLineRange(input)
			require.Error(err)
		})
	}
}
