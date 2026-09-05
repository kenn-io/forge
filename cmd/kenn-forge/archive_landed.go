package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"go.kenn.io/forge/internal/apiclient"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/archive/report"
	"go.kenn.io/forge/landedwork"
	"go.kenn.io/forge/platform"
)

func validateArchiveLandedOptions(opts archiveReportOptions) error {
	if !opts.landedWork {
		if opts.gitDirectory != "" || opts.baseSHA != "" || opts.headSHA != "" {
			return errors.New("git analysis flags require --landed-work")
		}
		return nil
	}
	if len(opts.repositories) != 1 || opts.gitDirectory == "" || opts.baseSHA == "" || opts.headSHA == "" {
		return errors.New("--landed-work requires exactly one --repo and explicit --git-dir, --base-sha and --head-sha")
	}
	for _, sha := range []string{opts.baseSHA, opts.headSHA} {
		decoded, err := hex.DecodeString(sha)
		if err != nil || len(decoded) != 20 && len(decoded) != 32 {
			return errors.New("--landed-work requires full hexadecimal commit SHAs")
		}
	}
	return nil
}

func collectArchiveLanded(ctx context.Context, client *apiclient.Client, opts archiveReportOptions, ref generated.ArchiveRepositoryRef, start, end time.Time) (report.LandedSection, error) {
	budget := platform.Budget{MaxRecords: report.MaxDetailedRecords, MaxNodes: report.MaxDetailedRecords, MaxBytes: report.MaxDetailedTextBytes, MaxOutputBytes: report.MaxDetailedTextBytes}
	meter, err := platform.NewMeter(ctx, budget)
	if err != nil {
		return report.LandedSection{}, err
	}
	response, err := client.HTTP.GetArchiveLandingEvidence(ctx, &generated.GetArchiveLandingEvidenceParams{Repo: archiveRepositoryFilters([]generated.ArchiveRepositoryRef{ref})[0]})
	if err != nil {
		return report.LandedSection{}, fmt.Errorf("landed-work evidence request: %w", err)
	}
	defer response.Body.Close()
	data, err := meter.Read(ctx, response.Body)
	if err != nil {
		return report.LandedSection{}, err
	}
	response.Body = io.NopCloser(bytes.NewReader(data))
	parsed, err := generated.ParseGetArchiveLandingEvidenceResponse(response)
	if err != nil {
		return report.LandedSection{}, err
	}
	if parsed.JSON200 == nil {
		return report.LandedSection{}, archiveAPIProblem("landed-work evidence", parsed.StatusCode(), parsed.ApplicationproblemJSONDefault)
	}
	snapshot, err := archiveLandingFromAPI(*parsed.JSON200)
	if err != nil {
		return report.LandedSection{}, err
	}
	host, err := platform.NormalizeHost(platform.Kind(ref.Provider), ref.PlatformHost)
	if err != nil || snapshot.Repository.Identity.Provider != platform.Kind(ref.Provider) || snapshot.Repository.Identity.Instance != host || snapshot.Repository.Identity.ID == "" {
		return report.LandedSection{}, errors.New("landed-work evidence does not match the selected provider instance")
	}
	source, err := landedwork.OpenGit(ctx, opts.gitDirectory, meter)
	if err != nil {
		return report.LandedSection{}, fmt.Errorf("open landed-work Git repository: %w", err)
	}
	defer source.Close()
	base, head := strings.ToLower(opts.baseSHA), strings.ToLower(opts.headSHA)
	correspondence, err := source.CheckCorrespondence(ctx, snapshot.Repository, head, meter)
	if err != nil {
		return report.LandedSection{}, fmt.Errorf("landed-work repository correspondence: %w", err)
	}
	request := landedwork.Request{Snapshot: snapshot, DefaultBranch: snapshot.Repository.DefaultBranch, BaseSHA: base, HeadSHA: head, Policy: landedwork.CodePolicy}
	var result landedwork.Result
	if correspondence.Complete {
		result, err = landedwork.Analyze(ctx, request, source, meter)
	} else {
		result = landedwork.Result{Schema: landedwork.Schema, Analyzer: landedwork.AnalyzerVersion, Policy: landedwork.CodePolicy, Repository: snapshot.Repository.Identity, BaseSHA: base, HeadSHA: head, CertifiedHead: base, Gaps: []landedwork.Gap{{FirstSHA: base, LastSHA: head, Reason: correspondence.Reason}}}
		result.Digest, err = landedwork.Digest(request, result)
	}
	if err != nil {
		return report.LandedSection{}, err
	}
	return report.BuildLandedSection(snapshot, result, correspondence, start, end), nil
}

func archiveLandingFromAPI(input generated.ArchiveLandingResponse) (platform.LandingSnapshot, error) {
	if input.SnapshotSchema != platform.LandingSnapshotSchema {
		return platform.LandingSnapshot{}, fmt.Errorf("unsupported landed-work snapshot schema %q", input.SnapshotSchema)
	}
	data, err := json.Marshal(input)
	if err != nil {
		return platform.LandingSnapshot{}, err
	}
	var snapshot platform.LandingSnapshot
	err = json.Unmarshal(data, &snapshot)
	return snapshot, err
}

func archiveLandedSectionFromAPI(input generated.LandedSection) (report.LandedSection, error) {
	data, err := json.Marshal(input)
	if err != nil {
		return report.LandedSection{}, err
	}
	var section report.LandedSection
	if err := json.Unmarshal(data, &section); err != nil {
		return report.LandedSection{}, err
	}
	if section.EvidenceSchema != landedwork.Schema || section.Evidence.Schema != landedwork.Schema {
		return report.LandedSection{}, errors.New("unsupported landed-work report schema")
	}
	if err := landedwork.WriteCanonicalEvidence(io.Discard, landedwork.Request{}, section.Evidence); err != nil {
		return report.LandedSection{}, fmt.Errorf("invalid landed-work byte evidence: %w", err)
	}
	return section, nil
}
