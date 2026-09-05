package report

import (
	"fmt"
	"strconv"

	"go.kenn.io/forge/landedwork"
	"go.kenn.io/forge/platform"
)

func writeLandedSection(out *outputBuffer, section *LandedSection) error {
	evidence := section.Evidence
	_, _ = out.WriteString("\n## Landed work\n\n")
	fmt.Fprintf(out, "Repository identity: %s / %s / %s\n\n", providerInline(string(evidence.Repository.Provider)), providerInline(evidence.Repository.Instance), providerInline(evidence.Repository.ID))
	fmt.Fprintf(out, "Schema: %s; analyzer: %s; code policy: %s\n\n", providerInline(section.EvidenceSchema), providerInline(evidence.Analyzer), providerInline(evidence.Policy))
	fmt.Fprintf(out, "Graph: %s to %s; provider-observed head: %s; certified head: %s\n\n", providerInline(evidence.BaseSHA), providerInline(evidence.HeadSHA), providerInline(section.Correspondence.ProviderHead), providerInline(evidence.CertifiedHead))
	fmt.Fprintf(out, "Provider observations: %s to %s\n\n", formatTime(section.Snapshot.StartedAt), formatTime(section.Snapshot.CompletedAt))
	label := "complete"
	if !section.Graph.Complete {
		label = "partial"
	}
	fmt.Fprintf(out, "### Graph interval (%s)\n\n", label)
	writeLandedMeasures(out, section.Graph)
	_, _ = out.WriteString("\n### Provider-time window\n\n")
	fmt.Fprintf(out, "%s to %s (exclusive); %d landings have no trusted time.\n\n", formatTime(section.ProviderTime.Start), formatTime(section.ProviderTime.End), section.ProviderTime.UnknownTimeLandings)
	writeLandedMeasures(out, section.ProviderTime.Measures)
	_, _ = out.WriteString("\n### Git-time window (unverified)\n\n")
	fmt.Fprintf(out, "%s to %s (exclusive); commit timestamps are declarations.\n\n", formatTime(section.GitTime.Start), formatTime(section.GitTime.End))
	writeLandedMeasures(out, section.GitTime.Measures)
	if section.Correspondence.Reason != "" {
		fmt.Fprintf(out, "\nCorrespondence gap: %s\n", providerInline(section.Correspondence.Reason))
	}
	if section.Snapshot.Coverage.Reason != "" {
		fmt.Fprintf(out, "\nProvider coverage: %s\n", providerInline(section.Snapshot.Coverage.Reason))
	}
	for _, warning := range section.Correspondence.Warnings {
		fmt.Fprintf(out, "\nWarning %s: remote %s; provider route %s\n", providerInline(warning.Reason), providerInline(warning.RemoteRoute), providerInline(warning.ProviderRoute))
	}
	for _, gap := range evidence.Gaps {
		fmt.Fprintf(out, "\nGap %s: %s to %s (change %s)\n", providerInline(gap.Reason), providerInline(gap.FirstSHA), providerInline(gap.LastSHA), providerInline(gap.ChangeID))
	}
	for _, landing := range evidence.Landings {
		fmt.Fprintf(out, "\n### Landing %s (%s)\n\n", providerInline(landing.Terminal), providerInline(landing.Origin))
		fmt.Fprintf(out, "Change: %s; method: %s\n\n", providerInline(landing.ChangeID), providerInline(string(landing.Method)))
		for _, claim := range landing.Claims {
			if err := writeLandedClaim(out, claim); err != nil {
				return err
			}
		}
		commits := append([]landedwork.CommitEvidence{landing.TerminalCommit}, landing.Sources...)
		commits = append(commits, landing.Introduced...)
		seen := make(map[string]bool)
		for _, commit := range commits {
			if seen[commit.ID] {
				continue
			}
			seen[commit.ID] = true
			fmt.Fprintf(out, "\nCommit %s; author time %s; committer time %s (unverified)\n\n", providerInline(commit.ID), formatTime(commit.AuthorTime), formatTime(commit.CommitterTime))
			for _, claim := range commit.Claims {
				if err := writeLandedClaim(out, claim); err != nil {
					return err
				}
			}
			for _, target := range commit.DeclaredReverts {
				fmt.Fprintf(out, "- Declared revert (unverified): %s\n", providerInline(target))
			}
		}
	}
	return nil
}

func writeLandedClaim(out *outputBuffer, claim landedwork.Claim) error {
	bytesText := func(value platform.RawBytes) (string, error) {
		decoded, err := value.Bytes()
		if err != nil {
			return "", err
		}
		// Quoting preserves invalid bytes and prevents terminal control output.
		return providerInline(strconv.QuoteToASCII(string(decoded))), nil
	}
	byline, err := bytesText(claim.RawByline)
	if err != nil {
		return err
	}
	email, err := bytesText(claim.Email)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "- %s / %s / %s: %s; email %s; provider identity %s/%s/%s\n", providerInline(string(claim.Role)), providerInline(string(claim.Kind)), providerInline(string(claim.Assurance)), byline, email, providerInline(string(claim.Provider)), providerInline(claim.Instance), providerInline(claim.ProviderUserID))
	return nil
}

func writeLandedMeasures(out *outputBuffer, measures LandedMeasures) {
	status := "complete"
	if !measures.Complete {
		status = "partial"
	}
	fmt.Fprintf(out, "- Coverage: %s\n- Change landings: %d\n- Direct-push landings: %d\n- Introduced non-merge commits: %d\n", status, measures.ChangeLandings, measures.DirectPushLandings, measures.IntroducedCommits)
	for _, churn := range []struct {
		name  string
		value *landedwork.LineCounts
	}{{"Raw", measures.RawChurn}, {"Code", measures.CodeChurn}} {
		if churn.value == nil {
			fmt.Fprintf(out, "- %s churn: unknown\n", churn.name)
		} else {
			fmt.Fprintf(out, "- %s churn: +%d -%d\n", churn.name, churn.value.Additions, churn.value.Deletions)
		}
	}
	for _, share := range []struct {
		name  string
		value *float64
	}{{"landing", measures.DirectPushLandingShare}, {"raw-churn", measures.DirectPushRawChurnShare}, {"code-churn", measures.DirectPushCodeChurnShare}} {
		if share.value == nil {
			fmt.Fprintf(out, "- Direct-push %s share: unknown\n", share.name)
		} else {
			fmt.Fprintf(out, "- Direct-push %s share: %.2f%%\n", share.name, *share.value*100)
		}
	}
}
