package report

import (
	"time"

	"go.kenn.io/forge/landedwork"
	"go.kenn.io/forge/platform"
)

type LandedMeasures struct {
	Complete                 bool                   `json:"complete"`
	ChangeLandings           int                    `json:"change_landings"`
	DirectPushLandings       int                    `json:"direct_push_landings"`
	IntroducedCommits        int                    `json:"introduced_commits"`
	RawChurn                 *landedwork.LineCounts `json:"raw_churn"`
	CodeChurn                *landedwork.LineCounts `json:"code_churn"`
	DirectPushLandingShare   *float64               `json:"direct_push_landing_share"`
	DirectPushRawChurnShare  *float64               `json:"direct_push_raw_churn_share"`
	DirectPushCodeChurnShare *float64               `json:"direct_push_code_churn_share"`
}

type LandedTimeView struct {
	Start               time.Time      `json:"start"`
	End                 time.Time      `json:"end"`
	Assurance           string         `json:"assurance"`
	UnknownTimeLandings int            `json:"unknown_time_landings"`
	Measures            LandedMeasures `json:"measures"`
}

type LandedSection struct {
	EvidenceSchema string                    `json:"schema"`
	Snapshot       platform.LandingSnapshot  `json:"snapshot"`
	Evidence       landedwork.Result         `json:"evidence"`
	Correspondence landedwork.Correspondence `json:"correspondence"`
	Graph          LandedMeasures            `json:"graph"`
	ProviderTime   LandedTimeView            `json:"provider_time"`
	GitTime        LandedTimeView            `json:"git_time"`
}

// BuildLandedSection never changes activity or contributor totals. Counts on an
// incomplete graph are measured subtotals, not estimates of the missing work.
func BuildLandedSection(snapshot platform.LandingSnapshot, evidence landedwork.Result, correspondence landedwork.Correspondence, start, end time.Time) LandedSection {
	complete := evidence.Complete && correspondence.Complete
	section := LandedSection{EvidenceSchema: landedwork.Schema, Snapshot: snapshot, Evidence: evidence, Correspondence: correspondence,
		Graph:        measureLandings(evidence.Landings, complete),
		ProviderTime: LandedTimeView{Start: start.UTC(), End: end.UTC(), Assurance: "provider"},
		GitTime:      LandedTimeView{Start: start.UTC(), End: end.UTC(), Assurance: "unverified"},
	}
	var providerWindow, gitWindow []landedwork.Landing
	within := func(at time.Time) bool { return !at.Before(start) && at.Before(end) }
	for _, landing := range evidence.Landings {
		if landing.Origin != "change" || landing.MergedAt == nil {
			// No current adapter supplies a trusted direct-push receipt. A Git
			// timestamp outside the window does not prove provider-time absence.
			section.ProviderTime.UnknownTimeLandings++
		} else if within(*landing.MergedAt) {
			providerWindow = append(providerWindow, landing)
		}
		if landing.TerminalCommit.CommitterTime.IsZero() {
			section.GitTime.UnknownTimeLandings++
		} else if within(landing.TerminalCommit.CommitterTime) {
			gitWindow = append(gitWindow, landing)
		}
	}
	section.ProviderTime.Measures = measureLandings(providerWindow, complete && section.ProviderTime.UnknownTimeLandings == 0)
	section.GitTime.Measures = measureLandings(gitWindow, complete && section.GitTime.UnknownTimeLandings == 0)
	return section
}

func measureLandings(landings []landedwork.Landing, complete bool) LandedMeasures {
	result := LandedMeasures{Complete: complete, RawChurn: new(landedwork.LineCounts), CodeChurn: new(landedwork.LineCounts)}
	var directRaw, directCode int64
	introduced := make(map[string]bool)
	for _, landing := range landings {
		direct := landing.Origin == "direct_push"
		if direct {
			result.DirectPushLandings++
		} else {
			result.ChangeLandings++
		}
		for _, commit := range landing.Introduced {
			introduced[commit.ID] = true
		}
		add := func(total **landedwork.LineCounts, value *landedwork.LineCounts, directTotal *int64) {
			if value == nil {
				*total = nil
				return
			}
			if *total != nil {
				(*total).Additions += value.Additions
				(*total).Deletions += value.Deletions
			}
			if direct {
				*directTotal += value.Additions + value.Deletions
			}
		}
		add(&result.RawChurn, landing.Churn.Raw, &directRaw)
		add(&result.CodeChurn, landing.Churn.Code, &directCode)
	}
	result.IntroducedCommits = len(introduced)
	if complete {
		if len(landings) > 0 {
			result.DirectPushLandingShare = new(float64(result.DirectPushLandings) / float64(len(landings)))
		}
		if value := result.RawChurn; value != nil && value.Additions+value.Deletions > 0 {
			result.DirectPushRawChurnShare = new(float64(directRaw) / float64(value.Additions+value.Deletions))
		}
		if value := result.CodeChurn; value != nil && value.Additions+value.Deletions > 0 {
			result.DirectPushCodeChurnShare = new(float64(directCode) / float64(value.Additions+value.Deletions))
		}
	}
	return result
}
