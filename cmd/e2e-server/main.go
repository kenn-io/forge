package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	gh "github.com/google/go-github/v84/github"
	gitcmd "go.kenn.io/kit/git/cmd"
	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/server"
	"go.kenn.io/middleman/internal/stacks"
	"go.kenn.io/middleman/internal/testutil"
	"go.kenn.io/middleman/internal/web"
)

// defaultRoborevEndpoint is the address the e2e server points the
// roborev proxy at when -roborev is not provided. It is deliberately
// an unbindable loopback port so direct playwright runs fail closed
// (the proxy returns 502) instead of silently forwarding test
// traffic to a real local roborev daemon (typically at
// 127.0.0.1:7373). The runner script (scripts/run-roborev-e2e.sh)
// always passes -roborev explicitly to the dockerized seeded daemon.
const defaultRoborevEndpoint = "http://127.0.0.1:1"

func main() {
	port := flag.Int("port", 0, "port to listen on (0 selects a random free port)")
	roborev := flag.String(
		"roborev", defaultRoborevEndpoint,
		"roborev daemon endpoint",
	)
	defaultPlatformHost := flag.String(
		"default-platform-host", "github.com",
		"default platform host for seeded config",
	)
	serverInfoFile := flag.String(
		"server-info-file", "",
		"path to write discovered server port info as JSON",
	)
	flag.Parse()

	ctx, stop := signal.NotifyContext(
		context.Background(),
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	defer stop()

	if err := run(
		ctx,
		*port,
		*roborev,
		*serverInfoFile,
		*defaultPlatformHost,
	); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

type e2eServerInfo struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	BaseURL    string `json:"base_url"`
	PID        int    `json:"pid"`
	ConfigPath string `json:"config_path"`
}

type e2eStaticProvider struct {
	kind        platform.Kind
	host        string
	caps        platform.Capabilities
	repos       []platform.Repository
	issue       platform.Issue
	issueEvents []platform.IssueEvent
}

func (p e2eStaticProvider) Platform() platform.Kind {
	return p.kind
}

func (p e2eStaticProvider) Host() string {
	return p.host
}

func (p e2eStaticProvider) Capabilities() platform.Capabilities {
	return p.caps
}

func (p e2eStaticProvider) GetRepository(
	_ context.Context,
	ref platform.RepoRef,
) (platform.Repository, error) {
	for _, repo := range p.repos {
		if repo.Ref.RepoPath == ref.RepoPath ||
			(repo.Ref.Owner == ref.Owner && repo.Ref.Name == ref.Name) {
			return repo, nil
		}
	}
	return platform.Repository{}, platform.ErrNotFound
}

func (p e2eStaticProvider) ListRepositories(
	_ context.Context,
	owner string,
	_ platform.RepositoryListOptions,
) ([]platform.Repository, error) {
	repos := make([]platform.Repository, 0, len(p.repos))
	for _, repo := range p.repos {
		if strings.EqualFold(repo.Ref.Owner, owner) {
			repos = append(repos, repo)
		}
	}
	return repos, nil
}

func (p e2eStaticProvider) ListOpenIssues(
	_ context.Context,
	ref platform.RepoRef,
) ([]platform.Issue, error) {
	if ref.RepoPath != p.issue.Repo.RepoPath {
		return nil, nil
	}
	if p.issue.State != "open" {
		return nil, nil
	}
	return []platform.Issue{p.issue}, nil
}

func (p e2eStaticProvider) GetIssue(
	_ context.Context,
	ref platform.RepoRef,
	number int,
) (platform.Issue, error) {
	if ref.RepoPath == p.issue.Repo.RepoPath && number == p.issue.Number {
		return p.issue, nil
	}
	return platform.Issue{}, fmt.Errorf("e2e static provider: issue %s#%d not found", ref.RepoPath, number)
}

func (p e2eStaticProvider) ListIssueEvents(
	_ context.Context,
	ref platform.RepoRef,
	number int,
) ([]platform.IssueEvent, error) {
	if ref.RepoPath == p.issue.Repo.RepoPath && number == p.issue.Number {
		return slices.Clone(p.issueEvents), nil
	}
	return nil, nil
}

type globRefreshContextKey struct{}

func e2eGit(ctx context.Context, dir string, args ...string) error {
	if len(args) == 0 {
		return fmt.Errorf("git: no args")
	}
	cmd := gitcmd.New().Command(ctx, dir, args...)
	cmd.Env = append(cmd.Env,
		"GIT_AUTHOR_DATE=2026-04-28T12:00:00Z",
		"GIT_COMMITTER_DATE=2026-04-28T12:00:00Z",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w: %s", args[0], err, stderr.String())
	}
	return nil
}

func createBareRepoFixture(ctx context.Context, tmpDir, host, owner, name string) (string, error) {
	workDir := filepath.Join(tmpDir, "fixture-work", host, owner, name)
	barePath := filepath.Join(tmpDir, "clones", host, owner, name+".git")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir fixture workdir: %w", err)
	}
	if err := e2eGit(ctx, workDir, "init", "-b", "main"); err != nil {
		return "", fmt.Errorf("init fixture repo: %w", err)
	}
	if err := e2eGit(ctx, workDir, "config", "user.email", "e2e@example.com"); err != nil {
		return "", fmt.Errorf("config fixture repo email: %w", err)
	}
	if err := e2eGit(ctx, workDir, "config", "user.name", "E2E Fixture"); err != nil {
		return "", fmt.Errorf("config fixture repo name: %w", err)
	}
	if err := os.WriteFile(
		filepath.Join(workDir, "README.md"),
		[]byte("# GitLab fixture\n"),
		0o644,
	); err != nil {
		return "", fmt.Errorf("write fixture file: %w", err)
	}
	if err := e2eGit(ctx, workDir, "add", "README.md"); err != nil {
		return "", fmt.Errorf("stage fixture repo: %w", err)
	}
	if err := e2eGit(ctx, workDir, "commit", "-m", "fixture: seed gitlab repo"); err != nil {
		return "", fmt.Errorf("commit fixture repo: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(barePath), 0o755); err != nil {
		return "", fmt.Errorf("mkdir bare fixture parent: %w", err)
	}
	if err := e2eGit(ctx, "", "clone", "--bare", workDir, barePath); err != nil {
		return "", fmt.Errorf("clone bare fixture repo: %w", err)
	}
	return barePath, nil
}

func gitLabReadOnlyRepoRef(cloneURL string) platform.RepoRef {
	return platform.RepoRef{
		Platform:      platform.KindGitLab,
		Host:          "gitlab.example.com",
		Owner:         "group",
		Name:          "project",
		RepoPath:      "group/project",
		WebURL:        "https://gitlab.example.com/group/project",
		CloneURL:      cloneURL,
		DefaultBranch: "main",
	}
}

func gitLabReadOnlyIssueFixture(
	now time.Time,
	cloneURL string,
) (platform.Issue, []platform.IssueEvent) {
	ref := gitLabReadOnlyRepoRef(cloneURL)
	issue := platform.Issue{
		Repo:         ref,
		PlatformID:   7101,
		Number:       11,
		URL:          "https://gitlab.example.com/group/project/-/issues/11",
		Title:        "GitLab read-only issue",
		Author:       "ada",
		State:        "open",
		Body:         "GitLab issue body",
		CommentCount: 1,
		CreatedAt:    now.Add(-48 * time.Hour),
		UpdatedAt:    now,
	}
	events := []platform.IssueEvent{
		{
			Repo:        ref,
			PlatformID:  7201,
			IssueNumber: 11,
			EventType:   "issue_comment",
			Author:      "ada",
			Body:        "GitLab read-only timeline comment",
			CreatedAt:   now,
			DedupeKey:   "gitlab-read-only-issue-comment",
		},
	}
	return issue, events
}

func seedLabelEditingFixture(
	ctx context.Context,
	database *db.DB,
	fc *testutil.FixtureClient,
) error {
	repo, err := database.GetRepoByOwnerName(ctx, "acme", "widgets")
	if err != nil {
		return fmt.Errorf("get widgets repo: %w", err)
	}
	if repo == nil {
		return nil
	}
	now := time.Now().UTC().Add(-time.Hour)
	catalog := []db.Label{
		{Name: "bug", Description: "Something is broken", Color: "d73a4a", IsDefault: true, UpdatedAt: now},
		{Name: "triage", Description: "Needs maintainer review", Color: "fbca04", UpdatedAt: now},
		{Name: "docs", Description: "Documentation", Color: "0075ca", UpdatedAt: now},
	}
	if err := database.ReplaceRepoLabelCatalog(ctx, repo.ID, catalog, now); err != nil {
		return fmt.Errorf("seed label catalog: %w", err)
	}
	if pr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 1); err != nil {
		return fmt.Errorf("get seeded pr: %w", err)
	} else if pr != nil {
		if err := database.ReplaceMergeRequestLabels(ctx, repo.ID, pr.ID, catalog[:1]); err != nil {
			return fmt.Errorf("seed pr labels: %w", err)
		}
	}
	if issue, err := database.GetIssueByRepoIDAndNumber(ctx, repo.ID, 10); err != nil {
		return fmt.Errorf("get seeded issue: %w", err)
	} else if issue != nil {
		if err := database.ReplaceIssueLabels(ctx, repo.ID, issue.ID, catalog[:1]); err != nil {
			return fmt.Errorf("seed issue labels: %w", err)
		}
	}
	seedFixtureClientLabels(fc)
	return nil
}

func seedFixtureClientLabels(fc *testutil.FixtureClient) {
	if fc == nil {
		return
	}
	bug := &gh.Label{
		ID:          new(int64(1)),
		NodeID:      new("LABEL_bug"),
		Name:        new("bug"),
		Description: new("Something is broken"),
		Color:       new("d73a4a"),
		Default:     new(true),
	}
	docs := &gh.Label{ID: new(int64(2)), NodeID: new("LABEL_docs"), Name: new("docs"), Description: new("Documentation"), Color: new("0075ca")}
	triage := &gh.Label{ID: new(int64(3)), NodeID: new("LABEL_triage"), Name: new("triage"), Description: new("Needs maintainer review"), Color: new("fbca04")}
	if fc.Labels == nil {
		fc.Labels = make(map[string][]*gh.Label)
	}
	fc.Labels["acme/widgets"] = []*gh.Label{bug, docs, triage}
	for _, prs := range [][]*gh.PullRequest{
		fc.OpenPRs["acme/widgets"],
		fc.PRs["acme/widgets"],
	} {
		for _, pr := range prs {
			if pr.GetNumber() == 1 {
				pr.Labels = []*gh.Label{bug}
			}
		}
	}
	for _, issues := range [][]*gh.Issue{
		fc.OpenIssues["acme/widgets"],
		fc.Issues["acme/widgets"],
	} {
		for _, issue := range issues {
			if issue.GetNumber() == 10 {
				issue.Labels = []*gh.Label{bug}
			}
		}
	}
}

func seedGitLabReadOnlyCapabilityFixture(
	ctx context.Context,
	database *db.DB,
	cloneURL string,
) error {
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	issue, events := gitLabReadOnlyIssueFixture(now, cloneURL)
	repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.example.com",
		PlatformRepoID: "7001",
		Owner:          issue.Repo.Owner,
		Name:           issue.Repo.Name,
		RepoPath:       issue.Repo.RepoPath,
	})
	if err != nil {
		return fmt.Errorf("upsert gitlab repo: %w", err)
	}
	issueID, err := database.UpsertIssue(ctx, &db.Issue{
		RepoID:          repoID,
		PlatformID:      issue.PlatformID,
		Number:          issue.Number,
		URL:             issue.URL,
		Title:           issue.Title,
		Author:          issue.Author,
		State:           issue.State,
		Body:            issue.Body,
		CommentCount:    issue.CommentCount,
		CreatedAt:       issue.CreatedAt,
		UpdatedAt:       issue.UpdatedAt,
		LastActivityAt:  now,
		DetailFetchedAt: &now,
	})
	if err != nil {
		return fmt.Errorf("upsert gitlab issue: %w", err)
	}
	commentID := events[0].PlatformID
	if err := database.UpsertIssueEvents(ctx, []db.IssueEvent{
		{
			IssueID:    issueID,
			PlatformID: &commentID,
			EventType:  events[0].EventType,
			Author:     events[0].Author,
			Body:       events[0].Body,
			CreatedAt:  events[0].CreatedAt,
			DedupeKey:  events[0].DedupeKey,
		},
	}); err != nil {
		return fmt.Errorf("upsert gitlab issue event: %w", err)
	}
	return nil
}

// ciFixtureOptions controls the per-fixture choices that the
// pr-ci-state/* endpoints feed into setPR1CIState. Centralising the
// options struct keeps the divergent fields visible and forces every
// fixture through the same anti-resync + provider-pin path.
type ciFixtureOptions struct {
	// statusName is the CIStatus column value ("failure", "success",
	// "pending", etc.).
	statusName string
	// checksJSON is the raw CIChecksJSON to seed. The empty string ""
	// writes an empty payload (the status-only fixture case). The
	// helper always writes this value to CIChecksJSON — there is no
	// "leave it alone" / "no-op" mode. If a transient state ever
	// needs to skip touching CIChecksJSON, add a new option flag
	// rather than overloading this field.
	checksJSON string
	// pinProviderTo optionally pins the fixture GitHub client's
	// check-run status/conclusion for PR #1 so a sync triggered by a
	// route transition can't overwrite the seeded payload. Nil means
	// don't touch the fixture provider.
	pinProviderTo *struct {
		Status     string
		Conclusion string
	}
	// providerCheckRuns replaces the fixture provider's check runs for
	// PR #1 when the test needs refreshes to preserve a multi-check payload.
	providerCheckRuns []*gh.CheckRun
}

func ciChecksToCheckRuns(checks []db.CICheck) []*gh.CheckRun {
	runs := make([]*gh.CheckRun, 0, len(checks))
	for _, check := range checks {
		name := check.Name
		status := check.Status
		conclusion := check.Conclusion
		url := check.URL
		app := check.App
		runs = append(runs, &gh.CheckRun{
			Name:       &name,
			Status:     &status,
			Conclusion: &conclusion,
			HTMLURL:    &url,
			App:        &gh.App{Name: &app},
		})
	}
	return runs
}

// setPR1CIState centralises the boilerplate shared by every
// /__e2e/pr-ci-state/* endpoint: repo lookup, CIStatus + CIChecksJSON
// write, the anti-resync detail_fetched_at stamp, an optional fixture
// check-run pin, and the JSON response. New endpoints reduce to a few
// lines of payload-building plus a single call here; the helper is the
// only place every fixture path converges so no future endpoint can
// forget the anti-resync stamp or the provider pin.
func setPR1CIState(
	w http.ResponseWriter,
	r *http.Request,
	database *db.DB,
	fc *testutil.FixtureClient,
	label string,
	opts ciFixtureOptions,
) {
	repo, err := database.GetRepoByOwnerName(r.Context(), "acme", "widgets")
	if err != nil || repo == nil {
		http.Error(w, "repo not found", http.StatusNotFound)
		return
	}
	if err := database.UpdateMRCIStatus(
		r.Context(), repo.ID, 1, opts.statusName, opts.checksJSON,
	); err != nil {
		http.Error(w, "update "+label+" CI", http.StatusInternalServerError)
		return
	}
	// Explicit anti-resync guarantee — every fixture stamps
	// detail_fetched_at with ci_had_pending=false so the sync engine
	// treats the seeded row as fresh and doesn't refetch + overwrite
	// it. Centralised here so no future endpoint can forget it.
	if err := database.UpdateMRDetailFetchedByRepoID(
		r.Context(), repo.ID, 1, false,
	); err != nil {
		http.Error(w, "mark "+label+" CI fetched", http.StatusInternalServerError)
		return
	}
	if opts.pinProviderTo != nil {
		if !fc.SetPullRequestCheckRunStatus(
			"acme", "widgets", 1,
			opts.pinProviderTo.Status, opts.pinProviderTo.Conclusion,
		) {
			http.Error(w, "update fixture check runs", http.StatusNotFound)
			return
		}
	}
	if len(opts.providerCheckRuns) > 0 {
		if !fc.SetPullRequestCheckRuns("acme", "widgets", 1, opts.providerCheckRuns) {
			http.Error(w, "replace fixture check runs", http.StatusNotFound)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{"status": label}); err != nil {
		slog.Warn("write e2e response", "err", err)
	}
}

// run starts the e2e server and blocks until ctx is canceled or the
// HTTP server errors out. Tests call it directly with a cancellable
// context; main() wires it to SIGINT/SIGTERM.
func run(
	ctx context.Context,
	port int,
	roborevEndpoint, serverInfoFile, defaultPlatformHost string,
) error {
	defaultPlatformHost = strings.TrimSpace(defaultPlatformHost)
	if defaultPlatformHost == "" {
		defaultPlatformHost = "github.com"
	}
	tmpDir, err := os.MkdirTemp("", "middleman-e2e-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	database, err := db.Open(tmpDir + "/e2e.db")
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	result, err := testutil.SeedFixtures(ctx, database)
	if err != nil {
		return fmt.Errorf("seed fixtures: %w", err)
	}
	gitLabCloneURL, err := createBareRepoFixture(
		ctx,
		tmpDir,
		"gitlab.example.com",
		"group",
		"project",
	)
	if err != nil {
		return fmt.Errorf("create gitlab fixture repo: %w", err)
	}
	if err := seedGitLabReadOnlyCapabilityFixture(ctx, database, gitLabCloneURL); err != nil {
		return fmt.Errorf("seed gitlab capability fixture: %w", err)
	}

	// Run stack detection so seeded stacked chains are discoverable
	// via /api/v1/stacks and the PR detail sidebar.
	for _, rp := range []struct{ owner, name string }{
		{"acme", "widgets"},
		{"acme", "tools"},
	} {
		repo, err := database.GetRepoByOwnerName(ctx, rp.owner, rp.name)
		if err != nil || repo == nil {
			continue
		}
		if err := stacks.RunDetection(ctx, database, repo.ID); err != nil {
			return fmt.Errorf("stack detection %s/%s: %w", rp.owner, rp.name, err)
		}
	}

	diffRepo, err := testutil.SetupDiffRepo(ctx, tmpDir, database)
	if err != nil {
		return fmt.Errorf("setup diff repo: %w", err)
	}

	repos := []config.Repo{
		{Owner: "acme", Name: "widgets"},
		{Owner: "acme", Name: "tools"},
		{Owner: "acme", Name: "archived"},
		{Owner: "roborev-dev", Name: "*"},
	}
	if !strings.EqualFold(defaultPlatformHost, "github.com") {
		repos = []config.Repo{
			{
				Owner:        "enterprise",
				Name:         "service",
				PlatformHost: defaultPlatformHost,
			},
			{
				Owner:        "acme",
				Name:         "widgets",
				PlatformHost: "github.com",
			},
		}
	}
	cfg := &config.Config{
		SyncInterval:        "5m",
		GitHubTokenEnv:      "MIDDLEMAN_GITHUB_TOKEN",
		DefaultPlatformHost: defaultPlatformHost,
		Host:                "127.0.0.1",
		Port:                8091,
		BasePath:            "/",
		Repos:               repos,
		Activity: config.Activity{
			ViewMode:  "flat",
			TimeRange: "7d",
		},
	}

	cfg.Roborev.Endpoint = roborevEndpoint
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate e2e config: %w", err)
	}
	cfgPath := filepath.Join(tmpDir, "config.toml")
	if err := cfg.Save(cfgPath); err != nil {
		return fmt.Errorf("save e2e config: %w", err)
	}

	fc := result.FixtureClient()
	if err := seedLabelEditingFixture(ctx, database, fc); err != nil {
		return fmt.Errorf("seed label editing fixture: %w", err)
	}
	fc.ListRepositoriesByOwnerFn = func(
		ctx context.Context, owner string,
	) ([]*gh.Repository, error) {
		pushedMiddleman := gh.Timestamp{Time: time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)}
		pushedWorker := gh.Timestamp{Time: time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC)}
		pushedBot := gh.Timestamp{Time: time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)}
		privateFalse := false
		if owner == "import-lab" {
			return []*gh.Repository{
				{
					Name:        new("api"),
					Owner:       &gh.User{Login: new(owner)},
					Description: new("Import API"),
					Private:     &privateFalse,
					Archived:    new(false),
					PushedAt:    &pushedMiddleman,
				},
				{
					Name:        new("worker"),
					Owner:       &gh.User{Login: new(owner)},
					Description: new("Import worker"),
					Private:     &privateFalse,
					Archived:    new(false),
					PushedAt:    &pushedWorker,
				},
				{
					Name:        new("archived"),
					Owner:       &gh.User{Login: new(owner)},
					Description: new("Archived import fixture"),
					Private:     &privateFalse,
					Archived:    new(true),
					PushedAt:    &pushedBot,
				},
			}, nil
		}
		if owner != "roborev-dev" {
			return fc.ReposByOwner[owner], nil
		}

		repos := []*gh.Repository{
			{
				Name:        new("middleman"),
				Owner:       &gh.User{Login: new(owner)},
				Description: new("Main dashboard"),
				Private:     &privateFalse,
				Archived:    new(false),
				PushedAt:    &pushedMiddleman,
			},
			{
				Name:        new("worker"),
				Owner:       &gh.User{Login: new(owner)},
				Description: new("Background jobs"),
				Private:     &privateFalse,
				Archived:    new(false),
				PushedAt:    &pushedWorker,
			},
			{
				Name:        new("archived"),
				Owner:       &gh.User{Login: new(owner)},
				Description: new("Archived service"),
				Private:     new(false),
				Archived:    new(true),
				PushedAt:    &pushedBot,
			},
		}
		if includeRefreshRepo, _ := ctx.Value(globRefreshContextKey{}).(bool); includeRefreshRepo {
			repos = append(repos, &gh.Repository{
				Name:        new("review-bot"),
				Owner:       &gh.User{Login: new(owner)},
				Description: new("Review automation"),
				Private:     &privateFalse,
				Archived:    new(false),
				PushedAt:    &pushedBot,
			})
		}
		return repos, nil
	}
	patchFixturePRSHAs(fc, "acme", "widgets", 1, diffRepo.HeadSHA, diffRepo.BaseSHA)

	fixtureClients := map[string]ghclient.Client{
		"github.com":        fc,
		defaultPlatformHost: fc,
	}
	startupResolved := ghclient.ResolveConfiguredRepos(
		ctx,
		fixtureClients,
		cfg.Repos,
	)
	for _, repo := range startupResolved.Expanded {
		if _, err := database.UpsertRepo(
			ctx, db.GitHubRepoIdentity(repo.PlatformHost, repo.Owner, repo.Name),
		); err != nil {
			return fmt.Errorf("seed startup repo %s/%s: %w", repo.Owner, repo.Name, err)
		}
	}
	if !strings.EqualFold(defaultPlatformHost, "github.com") {
		if _, err := database.UpsertRepo(
			ctx, db.GitHubRepoIdentity(defaultPlatformHost, "enterprise", "service"),
		); err != nil {
			return fmt.Errorf("seed default-host repo: %w", err)
		}
	}

	rt := ghclient.NewRateTracker(database, "github.com", "rest")
	// Seed with known values so the budget bars render.
	rt.UpdateFromRate(ghclient.Rate{
		Limit:     5000,
		Remaining: 4200,
		Reset:     time.Now().Add(45 * time.Minute),
	})

	gqlRT := ghclient.NewRateTracker(database, "github.com", "graphql")
	gqlRT.UpdateFromRate(ghclient.Rate{
		Limit:     5000,
		Remaining: 4800,
		Reset:     time.Now().Add(40 * time.Minute),
	})

	budget := ghclient.NewSyncBudget(500)
	budget.Spend(75)

	gitLabIssue, gitLabIssueEvents := gitLabReadOnlyIssueFixture(
		time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC),
		gitLabCloneURL,
	)
	forgeUpdated := time.Date(2026, 4, 25, 10, 0, 0, 0, time.UTC)
	giteaUpdated := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)
	registry, err := ghclient.NewProviderRegistry(
		fixtureClients,
		e2eStaticProvider{
			kind:        platform.KindGitLab,
			host:        "gitlab.example.com",
			issue:       gitLabIssue,
			issueEvents: gitLabIssueEvents,
			caps: platform.Capabilities{
				ReadIssues:   true,
				ReadComments: true,
			},
		},
		e2eStaticProvider{
			kind: platform.KindForgejo,
			host: "codeberg.org",
			caps: platform.Capabilities{
				ReadRepositories: true,
			},
			repos: []platform.Repository{
				{
					Ref: platform.RepoRef{
						Platform: platform.KindForgejo,
						Host:     "codeberg.org",
						Owner:    "forge-lab",
						Name:     "service",
						RepoPath: "forge-lab/service",
					},
					Description:   "Forgejo service",
					Private:       false,
					UpdatedAt:     forgeUpdated,
					DefaultBranch: "main",
					WebURL:        "https://codeberg.org/forge-lab/service",
					CloneURL:      "https://codeberg.org/forge-lab/service.git",
				},
				{
					Ref: platform.RepoRef{
						Platform: platform.KindForgejo,
						Host:     "codeberg.org",
						Owner:    "forge-lab",
						Name:     "archived",
						RepoPath: "forge-lab/archived",
					},
					Archived: true,
				},
			},
		},
		e2eStaticProvider{
			kind: platform.KindGitea,
			host: "gitea.com",
			caps: platform.Capabilities{
				ReadRepositories: true,
			},
			repos: []platform.Repository{
				{
					Ref: platform.RepoRef{
						Platform: platform.KindGitea,
						Host:     "gitea.com",
						Owner:    "gitea-team",
						Name:     "service",
						RepoPath: "gitea-team/service",
					},
					Description:   "Gitea service",
					Private:       false,
					UpdatedAt:     giteaUpdated,
					DefaultBranch: "main",
					WebURL:        "https://gitea.com/gitea-team/service",
					CloneURL:      "https://gitea.com/gitea-team/service.git",
				},
				{
					Ref: platform.RepoRef{
						Platform: platform.KindGitea,
						Host:     "gitea.com",
						Owner:    "gitea-team",
						Name:     "private-service",
						RepoPath: "gitea-team/private-service",
					},
					Description: "Private Gitea service",
					Private:     true,
					UpdatedAt:   giteaUpdated.Add(-time.Hour),
				},
				{
					Ref: platform.RepoRef{
						Platform: platform.KindGitea,
						Host:     "gitea.com",
						Owner:    "gitea-team",
						Name:     "archived",
						RepoPath: "gitea-team/archived",
					},
					Archived: true,
				},
			},
		},
	)
	if err != nil {
		return fmt.Errorf("create e2e provider registry: %w", err)
	}
	trackedRepos := append(
		slices.Clone(startupResolved.Expanded),
		ghclient.RepoRef{
			Platform:      platform.KindGitLab,
			PlatformHost:  gitLabIssue.Repo.Host,
			Owner:         gitLabIssue.Repo.Owner,
			Name:          gitLabIssue.Repo.Name,
			RepoPath:      gitLabIssue.Repo.RepoPath,
			WebURL:        gitLabIssue.Repo.WebURL,
			CloneURL:      gitLabIssue.Repo.CloneURL,
			DefaultBranch: gitLabIssue.Repo.DefaultBranch,
		},
	)
	syncer := ghclient.NewSyncerWithRegistry(
		registry,
		database, diffRepo.Manager, trackedRepos, time.Hour,
		map[string]*ghclient.RateTracker{
			"github.com":        rt,
			defaultPlatformHost: rt,
		},
		map[string]*ghclient.SyncBudget{
			"github.com":        budget,
			defaultPlatformHost: budget,
		},
	)

	// Wire GraphQL fetcher so GQL rate data appears in the endpoint.
	gqlFetcher := ghclient.NewGraphQLFetcher("fake-token", "github.com", gqlRT, budget)
	syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{
		"github.com":        gqlFetcher,
		defaultPlatformHost: gqlFetcher,
	})

	assets, err := web.Assets()
	if err != nil {
		return fmt.Errorf("load frontend assets: %w", err)
	}

	srv := server.NewWithConfig(
		database, syncer, diffRepo.Manager, assets, cfg, cfgPath,
		server.ServerOptions{
			Clones:      diffRepo.Manager,
			WorktreeDir: filepath.Join(tmpDir, "worktrees"),
		},
	)
	rootHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost &&
			r.URL.Path == "/__e2e/pr-workflow-approval/required" {
			repo, err := database.GetRepoByOwnerName(
				r.Context(), "acme", "widgets",
			)
			if err != nil || repo == nil {
				http.Error(w, "repo not found", http.StatusNotFound)
				return
			}
			pendingChecks, err := json.Marshal([]db.CICheck{{
				Name:       "build",
				Status:     "in_progress",
				Conclusion: "",
				URL:        "https://github.com/acme/widgets/actions/runs/1/job/1",
				App:        "GitHub Actions",
			}})
			if err != nil {
				http.Error(w, "marshal pending checks", http.StatusInternalServerError)
				return
			}
			if err := database.UpdateMRCIStatus(
				r.Context(), repo.ID, 1, "pending", string(pendingChecks),
			); err != nil {
				http.Error(w, "update pending CI", http.StatusInternalServerError)
				return
			}
			if err := database.UpdateMRDetailFetchedByRepoID(
				r.Context(), repo.ID, 1, true,
			); err != nil {
				http.Error(w, "mark pending CI fetched", http.StatusInternalServerError)
				return
			}

			headSHA := fc.PullRequestHeadSHA("acme", "widgets", 1)
			if headSHA == "" {
				http.Error(w, "PR head SHA not found", http.StatusNotFound)
				return
			}
			runID := int64(9001)
			event := "pull_request"
			number := 1
			fc.SetWorkflowRuns("acme", "widgets", headSHA, []*gh.WorkflowRun{{
				ID:           &runID,
				HeadSHA:      &headSHA,
				Event:        &event,
				PullRequests: []*gh.PullRequest{{Number: &number}},
			}})

			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]any{
				"status": "required",
				"run_id": runID,
			}); err != nil {
				slog.Warn("write e2e response", "err", err)
			}
			return
		}
		if r.Method == http.MethodPost &&
			r.URL.Path == "/__e2e/repo-settings/viewer-can-merge/deny" {
			repo, err := database.GetRepoByOwnerName(
				r.Context(), "acme", "widgets",
			)
			if err != nil || repo == nil {
				http.Error(w, "repo not found", http.StatusNotFound)
				return
			}
			if err := database.UpdateRepoViewerCanMerge(r.Context(), repo.ID, false); err != nil {
				http.Error(w, "update repo viewer merge permission", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]bool{
				"ViewerCanMerge": false,
			}); err != nil {
				slog.Warn("write e2e response", "err", err)
			}
			return
		}
		if r.Method == http.MethodPost &&
			r.URL.Path == "/__e2e/pr-ci-state/pending" {
			pendingPayload, err := json.Marshal([]db.CICheck{{
				Name:       "build",
				Status:     "in_progress",
				Conclusion: "",
				URL:        "https://github.com/acme/widgets/actions/runs/1/job/1",
				App:        "GitHub Actions",
			}})
			if err != nil {
				http.Error(w, "marshal pending checks", http.StatusInternalServerError)
				return
			}
			setPR1CIState(w, r, database, fc, "pending", ciFixtureOptions{
				statusName: "pending",
				checksJSON: string(pendingPayload),
				pinProviderTo: &struct {
					Status     string
					Conclusion string
				}{Status: "in_progress", Conclusion: ""},
			})
			return
		}
		if r.Method == http.MethodPost &&
			r.URL.Path == "/__e2e/pr-ci-state/fail-refresh" {
			if !fc.SetPullRequestCheckRunError(
				"acme", "widgets", 1, errors.New("fixture CI refresh failed"),
			) {
				http.Error(w, "set fixture check error", http.StatusNotFound)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]string{
				"status": "fail-refresh",
			}); err != nil {
				slog.Warn("write e2e response", "err", err)
			}
			return
		}
		if r.Method == http.MethodPost &&
			r.URL.Path == "/__e2e/pr-ci-state/success" {
			successPayload, err := json.Marshal([]db.CICheck{
				{
					Name:       "build",
					Status:     "completed",
					Conclusion: "success",
					URL:        "https://github.com/acme/widgets/actions/runs/1/job/1",
					App:        "GitHub Actions",
				},
				{
					Name:       "test",
					Status:     "completed",
					Conclusion: "success",
					App:        "GitHub Actions",
				},
			})
			if err != nil {
				http.Error(w, "marshal success checks", http.StatusInternalServerError)
				return
			}
			setPR1CIState(w, r, database, fc, "success", ciFixtureOptions{
				statusName: "success",
				checksJSON: string(successPayload),
				pinProviderTo: &struct {
					Status     string
					Conclusion string
				}{Status: "completed", Conclusion: "success"},
			})
			return
		}
		if r.Method == http.MethodPost &&
			r.URL.Path == "/__e2e/pr-ci-state/mixed" {
			mixedPayload, err := json.Marshal([]db.CICheck{
				{
					Name:       "build-darwin",
					Status:     "completed",
					Conclusion: "failure",
					URL:        "https://github.com/acme/widgets/actions/runs/1/job/1",
					App:        "GitHub Actions",
				},
				{
					Name:       "build-linux",
					Status:     "completed",
					Conclusion: "success",
					App:        "GitHub Actions",
				},
				{
					Name:       "test-linux",
					Status:     "completed",
					Conclusion: "success",
					App:        "GitHub Actions",
				},
				{
					Name:       "deploy-staging",
					Status:     "in_progress",
					Conclusion: "",
					App:        "GitHub Actions",
				},
				{
					Name:       "build-windows",
					Status:     "completed",
					Conclusion: "skipped",
					App:        "GitHub Actions",
				},
			})
			if err != nil {
				http.Error(w, "marshal mixed checks", http.StatusInternalServerError)
				return
			}
			setPR1CIState(w, r, database, fc, "mixed", ciFixtureOptions{
				statusName: "failure",
				checksJSON: string(mixedPayload),
				pinProviderTo: &struct {
					Status     string
					Conclusion string
				}{Status: "completed", Conclusion: "failure"},
			})
			return
		}
		if r.Method == http.MethodPost &&
			r.URL.Path == "/__e2e/pr-ci-state/malformed" {
			// No fixture-provider analogue for malformed JSON exists
			// — a real sync would replace the seeded text with a
			// valid array. The anti-resync stamp inside
			// setPR1CIState is the only defence.
			setPR1CIState(w, r, database, fc, "malformed", ciFixtureOptions{
				statusName: "failure",
				checksJSON: "{not json",
				// pinProviderTo intentionally nil
			})
			return
		}
		if r.Method == http.MethodPost &&
			r.URL.Path == "/__e2e/pr-ci-state/status-only" {
			// CIStatus is set but CIChecksJSON stays empty — exercises
			// the transient sync state where the redesigned UI hides
			// the chip. pinProviderTo stays nil so the provider can
			// remain aligned with the absent payload.
			setPR1CIState(w, r, database, fc, "status-only", ciFixtureOptions{
				statusName: "success",
				checksJSON: "",
				// pinProviderTo intentionally nil
			})
			return
		}
		if r.Method == http.MethodPost &&
			r.URL.Path == "/__e2e/pr-ci-state/dropdown-mixed" {
			// 21-check payload spanning every bucket so the dropdown
			// e2e can exercise the summary header, all five sections,
			// and the "Show N more passed" toggle (passed count of 12
			// exceeds the 8-row threshold).
			checks := []db.CICheck{
				{
					Name:       "build-darwin",
					Status:     "completed",
					Conclusion: "failure",
					App:        "GitHub Actions",
				},
			}
			for i := 1; i <= 5; i++ {
				checks = append(checks, db.CICheck{
					Name:       fmt.Sprintf("pending-%d", i),
					Status:     "completed",
					Conclusion: "",
					App:        "GitHub Actions",
				})
			}
			for i := 1; i <= 12; i++ {
				checks = append(checks, db.CICheck{
					Name:       fmt.Sprintf("passed-%d", i),
					Status:     "completed",
					Conclusion: "success",
					App:        "GitHub Actions",
				})
			}
			checks = append(checks,
				db.CICheck{
					Name:       "skip-1",
					Status:     "completed",
					Conclusion: "skipped",
					App:        "GitHub Actions",
				},
				db.CICheck{
					Name:       "skip-2",
					Status:     "completed",
					Conclusion: "skipped",
					App:        "GitHub Actions",
				},
				db.CICheck{
					Name:       "weird",
					Status:     "completed",
					Conclusion: "mysterious_state",
					App:        "GitHub Actions",
				},
			)
			dropdownPayload, err := json.Marshal(checks)
			if err != nil {
				http.Error(w, "marshal dropdown-mixed checks", http.StatusInternalServerError)
				return
			}
			setPR1CIState(w, r, database, fc, "dropdown-mixed", ciFixtureOptions{
				statusName:        "failure",
				checksJSON:        string(dropdownPayload),
				providerCheckRuns: ciChecksToCheckRuns(checks),
			})
			return
		}
		if r.Method == http.MethodPost &&
			r.URL.Path == "/__e2e/pr-diff-summary/advance-head" {
			repo, err := database.GetRepoByOwnerName(
				r.Context(), "acme", "widgets",
			)
			if err != nil || repo == nil {
				http.Error(w, "repo not found", http.StatusNotFound)
				return
			}
			if err := database.UpdateDiffSHAs(
				r.Context(), repo.ID, 1,
				diffRepo.AltHeadSHA, diffRepo.BaseSHA, diffRepo.BaseSHA,
			); err != nil {
				http.Error(w, "update diff shas", http.StatusInternalServerError)
				return
			}
			if err := database.UpdatePlatformSHAs(
				r.Context(), repo.ID, 1,
				diffRepo.AltHeadSHA, diffRepo.BaseSHA,
			); err != nil {
				http.Error(w, "update platform shas", http.StatusInternalServerError)
				return
			}
			patchFixturePRSHAs(
				fc, "acme", "widgets", 1,
				diffRepo.AltHeadSHA, diffRepo.BaseSHA,
			)
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]string{
				"head_sha": diffRepo.AltHeadSHA,
			}); err != nil {
				slog.Warn("write e2e response", "err", err)
			}
			return
		}
		if r.Method == http.MethodPost &&
			strings.Contains(r.URL.Path, "/api/v1/repo/") &&
			strings.Contains(r.URL.Path, "/roborev-dev/") &&
			strings.HasSuffix(r.URL.Path, "/refresh") {
			r = r.WithContext(
				context.WithValue(r.Context(), globRefreshContextKey{}, true),
			)
		}
		srv.ServeHTTP(w, r)
	})

	// Do not start the syncer's background loop. The seeded DB is the
	// ground truth for E2E tests; RunOnce would overwrite it with
	// incomplete fixture client data. The syncer only needs to exist
	// for Status() and IsTrackedRepo() calls.

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer listener.Close()

	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return fmt.Errorf("unexpected listener addr type %T", listener.Addr())
	}

	info := e2eServerInfo{
		Host:       "127.0.0.1",
		Port:       tcpAddr.Port,
		BaseURL:    fmt.Sprintf("http://127.0.0.1:%d", tcpAddr.Port),
		PID:        os.Getpid(),
		ConfigPath: cfgPath,
	}
	if err := writeServerInfoFile(serverInfoFile, info); err != nil {
		return fmt.Errorf("write server info file: %w", err)
	}
	defer cleanupServerInfoFile(serverInfoFile)

	slog.Info(fmt.Sprintf("starting e2e server at %s", info.BaseURL))

	httpServer := &http.Server{
		Handler:     rootHandler,
		ReadTimeout: 15 * time.Second,
		IdleTimeout: 60 * time.Second,
	}

	// Drain HTTP handlers and bg goroutines before DB close.
	// LIFO ordering: this runs after stop() but before the
	// deferred database.Close above. srv.Shutdown closes the
	// hub so SSE handlers exit, then drains bg goroutines;
	// httpServer.Shutdown drains in-flight HTTP handlers.
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(
			context.Background(), 10*time.Second,
		)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Warn("server shutdown", "err", err)
		}
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			slog.Warn("http shutdown", "err", err)
		}
	}()

	errCh := make(chan error, 1)
	go func() {
		if serveErr := httpServer.Serve(listener); !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutting down")
		// Trigger Shutdown so Serve unblocks (the defer is a
		// safety net for other exit paths and is idempotent).
		shutdownCtx, cancel := context.WithTimeout(
			context.Background(), 10*time.Second,
		)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Warn("server shutdown", "err", err)
		}
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			slog.Warn("http shutdown", "err", err)
		}
		// Drain errCh so a real Serve failure (not
		// ErrServerClosed) is surfaced instead of swallowed.
		if serveErr, ok := <-errCh; ok {
			return fmt.Errorf("server: %w", serveErr)
		}
		return nil
	case err := <-errCh:
		return fmt.Errorf("server: %w", err)
	}
}

func cleanupServerInfoFile(path string) {
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("cleanup server info file failed", "path", path, "err", err)
	}
}

func writeServerInfoFile(path string, info e2eServerInfo) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir server info dir: %w", err)
	}

	content, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("marshal server info: %w", err)
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, append(content, '\n'), 0o644); err != nil {
		return fmt.Errorf("write temp server info file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename server info file: %w", err)
	}
	return nil
}

func patchFixturePRSHAs(fc *testutil.FixtureClient, owner, repo string, number int, headSHA, baseSHA string) {
	if fc == nil {
		return
	}
	fc.UpdatePullRequestSHAs(owner, repo, number, headSHA, baseSHA)
}
