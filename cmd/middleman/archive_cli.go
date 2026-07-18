package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.kenn.io/middleman/internal/apiclient"
	"go.kenn.io/middleman/internal/apiclient/generated"
	"go.kenn.io/middleman/internal/archive/report"
	"go.kenn.io/middleman/internal/config"
)

const archiveCLIUsage = "usage: middleman archive <start|pause|reset|status|report> [flags]"

type archiveStringList []string

func (v *archiveStringList) String() string { return strings.Join(*v, ",") }
func (v *archiveStringList) Set(value string) error {
	*v = append(*v, value)
	return nil
}

type archiveDaemonFlags struct {
	configPath string
	timeout    time.Duration
}

func addArchiveDaemonFlags(fs *flag.FlagSet) *archiveDaemonFlags {
	flags := &archiveDaemonFlags{}
	fs.StringVar(&flags.configPath, "config", config.DefaultConfigPath(), "path to config file")
	fs.DurationVar(&flags.timeout, "timeout", 60*time.Second, "request timeout")
	return flags
}

func runArchiveCLI(args []string, stdout io.Writer) error {
	return runArchiveCLIAt(args, stdout, time.Now)
}

func runArchiveCLIAt(args []string, stdout io.Writer, now func() time.Time) error {
	if len(args) == 0 {
		return errors.New(archiveCLIUsage)
	}
	switch args[0] {
	case "start", "pause":
		return runArchiveMutation(args[0], args[1:], stdout)
	case "status":
		return runArchiveStatus(args[1:], stdout)
	case "reset":
		return runArchiveReset(args[1:])
	case "report":
		return runArchiveReport(args[1:], stdout, now)
	default:
		return fmt.Errorf("%s: unknown archive command %q", archiveCLIUsage, args[0])
	}
}

func runArchiveReset(args []string) error {
	fs := newArchiveFlagSet("middleman archive reset")
	daemonFlags := addArchiveDaemonFlags(fs)
	repository := fs.String("repo", "", "provider|host/repo_path")
	scan := fs.String("scan", "", "repository scan to reset")
	item := fs.String("item", "", "item scope as issue/N or merge_request/N")
	dataset := fs.String("dataset", "", "item dataset to reset")
	continueMode := fs.Bool("continue", false, "continue a page-bound scan from its cursor")
	force := fs.Bool("force", false, "reset progress that is not blocked")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *repository == "" {
		return errors.New("usage: middleman archive reset --repo provider|host/repo_path [--scan NAME | --item TYPE/N --dataset NAME] [--continue]")
	}
	if *scan != "" && *item != "" {
		return errors.New("--scan and --item are mutually exclusive")
	}
	if (*scan == "") == (*item == "") || (*item == "") != (*dataset == "") {
		return errors.New("select one --scan or one --item with --dataset")
	}
	ref, err := parseArchiveRepositoryRef(*repository)
	if err != nil {
		return fmt.Errorf("invalid --repo %q: %w", *repository, err)
	}
	body := generated.ArchiveResetBody{
		Repository: ref, Mode: "restart", Force: force,
	}
	if *continueMode {
		body.Mode = "continue"
	}
	if *scan != "" {
		body.Scan = scan
	} else {
		itemType, itemNumber, parseErr := parseArchiveResetItem(*item)
		if parseErr != nil {
			return parseErr
		}
		body.ItemType, body.ItemNumber, body.Dataset = &itemType, &itemNumber, dataset
	}
	client, err := newArchiveDaemonClient(daemonFlags)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), daemonFlags.timeout)
	defer cancel()
	response, err := client.HTTP.ResetArchiveScanWithResponse(ctx, body)
	if err != nil {
		return fmt.Errorf("archive reset request: %w", err)
	}
	if response.StatusCode() != http.StatusNoContent {
		return archiveAPIProblem("archive reset", response.StatusCode(), response.ApplicationproblemJSONDefault)
	}
	return nil
}

func parseArchiveResetItem(value string) (string, int64, error) {
	itemType, numberText, ok := strings.Cut(value, "/")
	if !ok || (itemType != "issue" && itemType != "merge_request") {
		return "", 0, errors.New("--item must be issue/N or merge_request/N")
	}
	number, err := strconv.ParseInt(numberText, 10, 64)
	if err != nil || number <= 0 {
		return "", 0, errors.New("--item number must be positive")
	}
	return itemType, number, nil
}

func runArchiveMutation(command string, args []string, stdout io.Writer) error {
	fs := newArchiveFlagSet("middleman archive " + command)
	daemonFlags := addArchiveDaemonFlags(fs)
	all := fs.Bool("all", false, "operate on every configured repository")
	var repositories archiveStringList
	fs.Var(&repositories, "repo", "provider|host/repo_path; repeat for multiple repositories")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: middleman archive %s [--all | --repo provider|host/repo_path]", command)
	}
	if *all && len(repositories) > 0 {
		return errors.New("--all and --repo are mutually exclusive")
	}
	if !*all && len(repositories) == 0 {
		return errors.New("one of --all or --repo is required")
	}
	refs, err := parseArchiveRepositoryRefs(repositories)
	if err != nil {
		return err
	}
	client, err := newArchiveDaemonClient(daemonFlags)
	if err != nil {
		return err
	}
	body := generated.ArchiveMutationBody{All: *all}
	if len(refs) > 0 {
		body.Repositories = &refs
	}
	ctx, cancel := context.WithTimeout(context.Background(), daemonFlags.timeout)
	defer cancel()
	var statuses []generated.ArchiveStatusResponse
	if command == "start" {
		response, requestErr := client.HTTP.StartArchivesWithResponse(ctx, body)
		if requestErr != nil {
			return fmt.Errorf("start archive request: %w", requestErr)
		}
		if response.JSON200 == nil {
			return archiveAPIProblem("start archive", response.StatusCode(), response.ApplicationproblemJSONDefault)
		}
		statuses = *response.JSON200
	} else {
		response, requestErr := client.HTTP.PauseArchivesWithResponse(ctx, body)
		if requestErr != nil {
			return fmt.Errorf("pause archive request: %w", requestErr)
		}
		if response.JSON200 == nil {
			return archiveAPIProblem("pause archive", response.StatusCode(), response.ApplicationproblemJSONDefault)
		}
		statuses = *response.JSON200
	}
	return writeArchiveJSON(stdout, statuses)
}

func runArchiveStatus(args []string, stdout io.Writer) error {
	fs := newArchiveFlagSet("middleman archive status")
	daemonFlags := addArchiveDaemonFlags(fs)
	var repositories archiveStringList
	_ = fs.Bool("json", false, "emit JSON (status output is always JSON)")
	fs.Var(&repositories, "repo", "provider|host/repo_path; repeat for multiple repositories")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: middleman archive status [--json] [--repo provider|host/repo_path]")
	}
	refs, err := parseArchiveRepositoryRefs(repositories)
	if err != nil {
		return err
	}
	client, err := newArchiveDaemonClient(daemonFlags)
	if err != nil {
		return err
	}
	params := &generated.ListArchiveStatusParams{}
	if len(refs) > 0 {
		values := archiveRepositoryFilters(refs)
		params.Repo = &values
	}
	ctx, cancel := context.WithTimeout(context.Background(), daemonFlags.timeout)
	defer cancel()
	response, err := client.HTTP.ListArchiveStatusWithResponse(ctx, params)
	if err != nil {
		return fmt.Errorf("archive status request: %w", err)
	}
	if response.JSON200 == nil {
		return archiveAPIProblem("archive status", response.StatusCode(), response.ApplicationproblemJSONDefault)
	}
	return writeArchiveJSON(stdout, *response.JSON200)
}

func runArchiveReport(args []string, stdout io.Writer, now func() time.Time) error {
	fs := newArchiveFlagSet("middleman archive report")
	daemonFlags := addArchiveDaemonFlags(fs)
	days := fs.Int("days", 0, "rolling number of 24-hour UTC periods")
	startValue := fs.String("start", "", "inclusive UTC date or RFC3339 boundary")
	endValue := fs.String("end", "", "inclusive UTC date or exclusive RFC3339 boundary")
	format := fs.String("format", "markdown", "output format: markdown or json")
	verbose := fs.Bool("verbose", false, "include bounded activity details")
	output := fs.String("output", "", "write output atomically to this file")
	var repositories archiveStringList
	fs.Var(&repositories, "repo", "provider|host/repo_path; repeat for multiple repositories")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: middleman archive report (--days N | --start VALUE --end VALUE) [flags]")
	}
	if archiveFlagWasSet(fs, "days") && *days <= 0 {
		return errors.New("--days must be positive")
	}
	if *format != "markdown" && *format != "json" {
		return fmt.Errorf("unsupported archive report format %q; use markdown or json", *format)
	}
	start, end, err := parseArchiveReportRange(now().UTC(), *days, *startValue, *endValue)
	if err != nil {
		return err
	}
	refs, err := parseArchiveRepositoryRefs(repositories)
	if err != nil {
		return err
	}
	client, err := newArchiveDaemonClient(daemonFlags)
	if err != nil {
		return err
	}
	params := &generated.GetArchiveReportParams{
		Start: start.Format(time.RFC3339), End: end.Format(time.RFC3339),
	}
	if len(refs) > 0 {
		values := archiveRepositoryFilters(refs)
		params.Repo = &values
	}
	if *verbose {
		params.Verbose = verbose
	}
	ctx, cancel := context.WithTimeout(context.Background(), daemonFlags.timeout)
	defer cancel()
	response, err := client.HTTP.GetArchiveReportWithResponse(ctx, params)
	if err != nil {
		return fmt.Errorf("archive report request: %w", err)
	}
	if response.JSON200 == nil {
		return archiveAPIProblem("archive report", response.StatusCode(), response.ApplicationproblemJSONDefault)
	}
	model, err := archiveReportFromAPI(*response.JSON200)
	if err != nil {
		return fmt.Errorf("decode archive report: %w", err)
	}
	rendered, err := renderArchiveReport(model, *format)
	if err != nil {
		return err
	}
	if *output != "" {
		return writeArchiveOutput(*output, rendered)
	}
	_, err = io.WriteString(stdout, rendered)
	return err
}

func newArchiveFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func archiveFlagWasSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(item *flag.Flag) {
		if item.Name == name {
			found = true
		}
	})
	return found
}

func newArchiveDaemonClient(flags *archiveDaemonFlags) (*apiclient.Client, error) {
	daemon, err := discoverDaemonHTTP(flags.configPath, flags.timeout)
	if err != nil {
		return nil, err
	}
	client, err := apiclient.NewWithHTTPClient(daemon.BaseURL, daemon.Client)
	if err != nil {
		return nil, fmt.Errorf("create archive API client: %w", err)
	}
	return client, nil
}

func parseArchiveRepositoryRefs(values []string) ([]generated.ArchiveRepositoryRef, error) {
	refs := make([]generated.ArchiveRepositoryRef, len(values))
	for i, value := range values {
		ref, err := parseArchiveRepositoryRef(value)
		if err != nil {
			return nil, fmt.Errorf("invalid --repo %q: %w", value, err)
		}
		refs[i] = ref
	}
	return refs, nil
}

func archiveRepositoryFilters(refs []generated.ArchiveRepositoryRef) []string {
	values := make([]string, len(refs))
	for i, ref := range refs {
		values[i] = ref.Provider + "|" + ref.PlatformHost + "/" + ref.RepoPath
	}
	return values
}

func parseArchiveRepositoryRef(value string) (generated.ArchiveRepositoryRef, error) {
	provider, remainder, ok := strings.Cut(strings.TrimSpace(value), "|")
	provider = strings.TrimSpace(provider)
	if !ok || provider == "" {
		return generated.ArchiveRepositoryRef{}, errors.New("expected provider|host/repo_path")
	}
	host, repoPath, ok := strings.Cut(remainder, "/")
	host = strings.TrimSpace(host)
	repoPath = strings.TrimSpace(repoPath)
	if !ok || host == "" || repoPath == "" {
		return generated.ArchiveRepositoryRef{}, errors.New("expected provider|host/repo_path")
	}
	lastSlash := strings.LastIndex(repoPath, "/")
	if lastSlash <= 0 || lastSlash == len(repoPath)-1 {
		return generated.ArchiveRepositoryRef{}, errors.New("expected provider|host/repo_path")
	}
	return generated.ArchiveRepositoryRef{
		Provider: provider, PlatformHost: host, Owner: repoPath[:lastSlash],
		Name: repoPath[lastSlash+1:], RepoPath: repoPath,
	}, nil
}

func parseArchiveReportRange(now time.Time, days int, startValue, endValue string) (time.Time, time.Time, error) {
	startValue = strings.TrimSpace(startValue)
	endValue = strings.TrimSpace(endValue)
	if days != 0 && (startValue != "" || endValue != "") {
		return time.Time{}, time.Time{}, errors.New("--days and --start/--end are mutually exclusive")
	}
	if days != 0 {
		if days < 0 {
			return time.Time{}, time.Time{}, errors.New("--days must be positive")
		}
		end := now.UTC()
		return end.Add(-time.Duration(days) * 24 * time.Hour), end, nil
	}
	if startValue == "" || endValue == "" {
		return time.Time{}, time.Time{}, errors.New("provide --days or both --start and --end")
	}
	startDate, startDateErr := time.Parse(time.DateOnly, startValue)
	endDate, endDateErr := time.Parse(time.DateOnly, endValue)
	dateOnly := startDateErr == nil && endDateErr == nil
	if (startDateErr == nil) != (endDateErr == nil) {
		return time.Time{}, time.Time{}, errors.New("--start and --end must use the same form: dates or RFC3339")
	}
	var start, end time.Time
	if dateOnly {
		start = startDate.UTC()
		end = endDate.UTC().Add(24 * time.Hour)
	} else {
		var err error
		start, err = time.Parse(time.RFC3339, startValue)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("parse --start as UTC date or RFC3339: %w", err)
		}
		end, err = time.Parse(time.RFC3339, endValue)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("parse --end as UTC date or RFC3339: %w", err)
		}
		start = start.UTC()
		end = end.UTC()
	}
	if !start.Before(end) {
		return time.Time{}, time.Time{}, errors.New("archive report start must precede end")
	}
	return start, end, nil
}

func archiveReportFromAPI(input generated.ArchiveReportResponse) (report.Model, error) {
	model := report.Model{Start: input.Start.UTC(), End: input.End.UTC()}
	if input.Repositories != nil {
		model.Repositories = make([]report.Repository, len(*input.Repositories))
		for i, item := range *input.Repositories {
			model.Repositories[i] = report.Repository{
				Repository: archiveReportRepositoryRefFromAPI(item.Repository),
				Coverage:   archiveReportCoverageFromAPI(item.Coverage),
				Counts:     archiveReportCountsFromAPI(item.Counts),
			}
		}
	} else {
		model.Repositories = []report.Repository{}
	}
	model.Totals = archiveReportCountsFromAPI(input.Totals)
	if input.Contributors != nil {
		model.Contributors = make([]report.Contributor, len(*input.Contributors))
		for i, item := range *input.Contributors {
			model.Contributors[i] = report.Contributor{
				Provider: item.Provider, PlatformHost: item.PlatformHost,
				Login: item.Login, Counts: archiveReportCountsFromAPI(item.Counts),
			}
		}
	} else {
		model.Contributors = []report.Contributor{}
	}
	if input.Activity != nil {
		model.Activity = make([]report.Activity, len(*input.Activity))
		for i, item := range *input.Activity {
			kind := report.ActivityKind(item.Kind)
			if !validArchiveActivityKind(kind) {
				return report.Model{}, fmt.Errorf("unknown activity kind %q", item.Kind)
			}
			model.Activity[i] = report.Activity{
				Repository: archiveReportRepositoryRefFromAPI(item.Repository), Kind: kind,
				ItemNumber: int(item.ItemNumber), ProviderExternalID: item.ProviderExternalId,
				Title: item.Title, Author: item.Author, OccurredAt: item.OccurredAt.UTC(),
				Body: item.Body, URL: item.Url,
			}
		}
	}
	return model, nil
}

func archiveReportRepositoryRefFromAPI(input generated.ArchiveRepositoryRef) report.RepositoryRef {
	return report.RepositoryRef{
		Provider: input.Provider, PlatformHost: input.PlatformHost, Owner: input.Owner,
		Name: input.Name, RepoPath: input.RepoPath,
	}
}

func archiveReportCountsFromAPI(input generated.ArchiveReportCountsResponse) report.Counts {
	return report.Counts{
		IssuesOpened: int(input.IssuesOpened), MergeRequestsOpened: int(input.MergeRequestsOpened),
		OrdinaryComments: int(input.OrdinaryComments), ReviewsSubmitted: int(input.ReviewsSubmitted),
		InlineReviewComments: int(input.InlineReviewComments),
	}
}

func archiveReportCoverageFromAPI(input generated.ArchiveReportCoverageResponse) report.Coverage {
	phases := []string{}
	if input.ActivePhases != nil {
		phases = append(phases, (*input.ActivePhases)...)
	}
	return report.Coverage{
		Status: string(input.Status), ActivePhases: phases,
		CollectionMode: string(input.CollectionMode), OperatorState: string(input.OperatorState),
		Comments: string(input.Comments), Reviews: string(input.Reviews),
		InlineComments: string(input.InlineComments), InitialCompletedAt: input.InitialCompletedAt,
		MaintenanceSucceededAt: input.MaintenanceSucceededAt,
		BudgetWaitUntil:        input.BudgetWaitUntil, ArchivedItems: int(input.ArchivedItems),
		UnsupportedItems: int(input.UnsupportedItems), InaccessibleItems: int(input.InaccessibleItems),
	}
}

func validArchiveActivityKind(kind report.ActivityKind) bool {
	switch kind {
	case report.ActivityIssue, report.ActivityMergeRequest, report.ActivityOrdinaryComment,
		report.ActivityReview, report.ActivityInlineReviewComment:
		return true
	default:
		return false
	}
}

func renderArchiveReport(model report.Model, format string) (string, error) {
	if format == "markdown" {
		return report.RenderMarkdown(model)
	}
	data, err := json.MarshalIndent(model, "", "  ")
	if err != nil {
		return "", fmt.Errorf("render archive report JSON: %w", err)
	}
	return string(data) + "\n", nil
}

func writeArchiveJSON(output io.Writer, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("render archive JSON: %w", err)
	}
	_, err = fmt.Fprintf(output, "%s\n", data)
	return err
}

func writeArchiveOutput(path string, contents string) (err error) {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create archive output temp file: %w", err)
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		if err != nil {
			_ = os.Remove(tempPath)
		}
	}()
	if _, err = io.WriteString(temp, contents); err != nil {
		return fmt.Errorf("write archive output temp file: %w", err)
	}
	if err = temp.Sync(); err != nil {
		return fmt.Errorf("sync archive output temp file: %w", err)
	}
	if err = temp.Close(); err != nil {
		return fmt.Errorf("close archive output temp file: %w", err)
	}
	if err = os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace archive output: %w", err)
	}
	return nil
}

func archiveAPIProblem(operation string, status int, problem *generated.ProblemError) error {
	if problem == nil {
		return fmt.Errorf("%s failed with HTTP %d", operation, status)
	}
	if problem.Code == generated.PayloadTooLarge && archiveProblemReason(problem) == "reportTooLarge" {
		return errors.New("archive report is too large; narrow the UTC range or repository scope")
	}
	if problem.Details != nil && len(*problem.Details) > 0 {
		details, err := json.Marshal(*problem.Details)
		if err == nil {
			return fmt.Errorf("%s failed with HTTP %d (%s; details=%s)", operation, status, problem.Code, details)
		}
	}
	return fmt.Errorf("%s failed with HTTP %d (%s)", operation, status, problem.Code)
}

func archiveProblemReason(problem *generated.ProblemError) string {
	if problem.Details == nil {
		return ""
	}
	reason, _ := (*problem.Details)["reason"].(string)
	return reason
}
