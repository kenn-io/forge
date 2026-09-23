// Package ghshim implements the deliberately narrow gh JSON compatibility contract.
package ghshim

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
	"slices"
	"strconv"
	"strings"

	gh "github.com/google/go-github/v91/github"
	"github.com/spf13/pflag"
)

// Query contains only combinations whose semantics match gh. Unknown flags and
// fields must delegate the entire original invocation, never a partial result.
type Query struct {
	Command string   `json:"command"`
	Host    string   `json:"host"`
	Owner   string   `json:"owner"`
	Repo    string   `json:"repo"`
	Number  int      `json:"number"`
	State   string   `json:"state"`
	Head    string   `json:"head"`
	Base    string   `json:"base"`
	Limit   int      `json:"limit"`
	Fields  []string `json:"fields"`
}

const Fields = "number,title,state,url,body,isDraft,headRefName,headRefOid,baseRefName,createdAt,updatedAt,closedAt,mergedAt"

func Parse(args []string) (Query, string, bool) {
	q := Query{State: "open", Limit: 30}
	if len(args) < 2 || args[0] != "pr" || !slices.Contains([]string{"list", "ls", "view"}, args[1]) {
		return q, "", false
	}
	q.Command = args[1]
	if q.Command == "ls" {
		q.Command = "list"
	}
	flags := pflag.NewFlagSet("gh", pflag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var repo string
	flags.StringVarP(&repo, "repo", "R", "", "")
	flags.StringSliceVar(&q.Fields, "json", nil, "")
	if q.Command == "list" {
		flags.StringVarP(&q.State, "state", "s", "open", "")
		flags.StringVarP(&q.Head, "head", "H", "", "")
		flags.StringVarP(&q.Base, "base", "B", "", "")
		flags.IntVarP(&q.Limit, "limit", "L", 30, "")
	}
	if flags.Parse(args[2:]) != nil {
		return q, repo, false
	}
	if q.Command == "view" {
		if flags.NArg() != 1 {
			return q, repo, false
		}
		var err error
		q.Number, err = strconv.Atoi(flags.Arg(0))
		if err != nil || q.Number <= 0 {
			return q, repo, false
		}
	} else if flags.NArg() != 0 {
		return q, repo, false
	}
	return q, repo, q.Valid()
}

func (q Query) Valid() bool {
	if q.Command != "list" && q.Command != "view" {
		return false
	}
	if (q.Command == "view" && q.Number <= 0) || (q.Command == "list" && q.Number != 0) {
		return false
	}
	if q.Limit < 1 || q.Limit > 1000 || !slices.Contains([]string{"open", "closed", "merged", "all"}, q.State) {
		return false
	}
	if len(q.Fields) == 0 {
		return false
	}
	allowed := strings.Split(Fields, ",")
	for _, field := range q.Fields {
		if !slices.Contains(allowed, field) {
			return false
		}
	}
	return true
}

// Encode mirrors cli/cli's api.PullRequest.ExportData and jsonExporter.Write:
// sorted keys, literal HTML characters, null optional dates, and one newline.
// Explicit options retain gh's byte format with the repository's v2 encoder.
func Encode(q Query, pulls []*gh.PullRequest) ([]byte, error) {
	rows := make([]map[string]any, 0, len(pulls))
	for _, pr := range pulls {
		state := strings.ToUpper(pr.GetState())
		if pr.MergedAt != nil {
			state = "MERGED"
		}
		values := map[string]any{
			"number": pr.GetNumber(), "title": pr.GetTitle(), "body": pr.GetBody(), "state": state, "url": pr.GetHTMLURL(),
			"isDraft": pr.GetDraft(), "headRefName": pr.GetHead().GetRef(), "headRefOid": pr.GetHead().GetSHA(), "baseRefName": pr.GetBase().GetRef(),
			"createdAt": pr.GetCreatedAt().Time, "updatedAt": pr.GetUpdatedAt().Time, "closedAt": pr.ClosedAt, "mergedAt": pr.MergedAt,
		}
		row := make(map[string]any, len(q.Fields))
		for _, f := range q.Fields {
			row[f] = values[f]
		}
		rows = append(rows, row)
	}
	var value any = rows
	if q.Command == "view" {
		if len(rows) != 1 {
			return nil, io.EOF
		}
		value = rows[0]
	}
	data, err := json.Marshal(value, json.Deterministic(true), jsontext.EscapeForJS(true))
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
