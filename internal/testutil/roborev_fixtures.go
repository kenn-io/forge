package testutil

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// roborevSchema contains the CREATE TABLE and CREATE INDEX statements
// from roborev's storage layer. Only the tables needed for the daemon
// API are included (repos, commits, review_jobs, reviews, responses).
const roborevSchema = `
CREATE TABLE IF NOT EXISTS repos (
  id INTEGER PRIMARY KEY,
  root_path TEXT UNIQUE NOT NULL,
  name TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS commits (
  id INTEGER PRIMARY KEY,
  repo_id INTEGER NOT NULL REFERENCES repos(id),
  sha TEXT NOT NULL,
  author TEXT NOT NULL,
  subject TEXT NOT NULL,
  timestamp TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  UNIQUE(repo_id, sha)
);

CREATE TABLE IF NOT EXISTS review_jobs (
  id INTEGER PRIMARY KEY,
  repo_id INTEGER NOT NULL REFERENCES repos(id),
  commit_id INTEGER REFERENCES commits(id),
  git_ref TEXT NOT NULL,
  branch TEXT,
  session_id TEXT,
  agent TEXT NOT NULL DEFAULT 'codex',
  model TEXT,
  requested_model TEXT,
  requested_provider TEXT,
  reasoning TEXT NOT NULL DEFAULT 'thorough',
  status TEXT NOT NULL CHECK(status IN (
    'queued','running','done','failed','canceled','applied','rebased'
  )) DEFAULT 'queued',
  enqueued_at TEXT NOT NULL DEFAULT (datetime('now')),
  started_at TEXT,
  finished_at TEXT,
  worker_id TEXT,
  error TEXT,
  prompt TEXT,
  retry_count INTEGER NOT NULL DEFAULT 0,
  diff_content TEXT,
  output_prefix TEXT,
  job_type TEXT NOT NULL DEFAULT 'review',
  review_type TEXT NOT NULL DEFAULT '',
  provider TEXT,
  token_usage TEXT,
  uuid TEXT,
  agentic INTEGER NOT NULL DEFAULT 0,
  patch_id TEXT,
  parent_job_id INTEGER,
  patch TEXT,
  worktree_path TEXT DEFAULT '',
  prompt_prebuilt INTEGER NOT NULL DEFAULT 0,
  source_machine_id TEXT,
  updated_at TEXT,
  synced_at TEXT
);

CREATE TABLE IF NOT EXISTS reviews (
  id INTEGER PRIMARY KEY,
  job_id INTEGER UNIQUE NOT NULL REFERENCES review_jobs(id),
  agent TEXT NOT NULL,
  prompt TEXT NOT NULL,
  output TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  closed INTEGER NOT NULL DEFAULT 0,
  verdict_bool INTEGER,
  uuid TEXT,
  updated_at TEXT,
  updated_by_machine_id TEXT,
  synced_at TEXT
);

CREATE TABLE IF NOT EXISTS responses (
  id INTEGER PRIMARY KEY,
  commit_id INTEGER REFERENCES commits(id),
  job_id INTEGER REFERENCES review_jobs(id),
  responder TEXT NOT NULL,
  response TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  uuid TEXT,
  source_machine_id TEXT,
  synced_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_review_jobs_status
  ON review_jobs(status);
CREATE INDEX IF NOT EXISTS idx_review_jobs_repo
  ON review_jobs(repo_id);
CREATE INDEX IF NOT EXISTS idx_review_jobs_git_ref
  ON review_jobs(git_ref);
CREATE INDEX IF NOT EXISTS idx_review_jobs_branch
  ON review_jobs(branch);
CREATE INDEX IF NOT EXISTS idx_commits_sha
  ON commits(sha);
CREATE INDEX IF NOT EXISTS idx_reviews_closed
  ON reviews(closed);
CREATE INDEX IF NOT EXISTS idx_reviews_verdict_bool
  ON reviews(verdict_bool);
CREATE INDEX IF NOT EXISTS idx_responses_job_id
  ON responses(job_id);
`

const roborevTimeFmt = "2006-01-02T15:04:05Z"

// roborevBaseTime is 2026-03-30 00:00:00 UTC, giving a 7-day spread.
var roborevBaseTime = time.Date(
	2026, time.March, 30, 0, 0, 0, 0, time.UTC,
)

// SeedRoborevDB creates a roborev-compatible SQLite database at path
// with deterministic test data. The database contains ~75 review jobs
// across 2 repos, 5 branches, 3 agents, and all statuses.
func SeedRoborevDB(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	db, err := sql.Open(
		"sqlite", path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	if _, err := db.Exec(roborevSchema); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil && err != sql.ErrTxDone {
			return
		}
	}()

	if err := seedRoborevRepos(tx); err != nil {
		return err
	}
	if err := seedRoborevCommits(tx); err != nil {
		return err
	}
	if err := seedRoborevBulkJobs(tx); err != nil {
		return err
	}
	if err := seedRoborevMutationFixtures(tx); err != nil {
		return err
	}
	if err := seedRoborevReviews(tx); err != nil {
		return err
	}
	if err := seedRoborevResponses(tx); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

func seedRoborevRepos(tx *sql.Tx) error {
	repos := []struct {
		id   int
		path string
		name string
	}{
		{1, "/home/dev/test-repo-alpha", "test-repo-alpha"},
		{2, "/home/dev/test-repo-beta", "test-repo-beta"},
	}
	for _, r := range repos {
		_, err := tx.Exec(
			`INSERT INTO repos (id, root_path, name, created_at)
			 VALUES (?, ?, ?, ?)`,
			r.id, r.path, r.name,
			roborevBaseTime.Format(roborevTimeFmt),
		)
		if err != nil {
			return fmt.Errorf("insert repo %s: %w", r.name, err)
		}
	}
	return nil
}

func seedRoborevCommits(tx *sql.Tx) error {
	for i := 1; i <= 75; i++ {
		repoID := 1
		if i > 45 {
			repoID = 2
		}
		sha := fmt.Sprintf("%08x", 0xaa000000+i)
		subject := fmt.Sprintf("commit %d: implement feature", i)
		ts := jobTime(i)
		_, err := tx.Exec(
			`INSERT INTO commits
			 (id, repo_id, sha, author, subject,
			  timestamp, created_at)
			 VALUES (?, ?, ?, 'dev-user', ?, ?, ?)`,
			i, repoID, sha, subject,
			ts.Format(roborevTimeFmt),
			ts.Format(roborevTimeFmt),
		)
		if err != nil {
			return fmt.Errorf("insert commit %d: %w", i, err)
		}
	}
	return nil
}

// Bulk jobs: IDs 1-69
// IDs 1-45 in test-repo-alpha, IDs 46-69 in test-repo-beta.
// Mutation fixtures (70-74) are inserted separately; we skip
// IDs 70+ in the bulk insert to avoid conflicts.
func seedRoborevBulkJobs(tx *sql.Tx) error {
	type agentSpec struct {
		name  string
		model string
	}
	agents := []agentSpec{
		{"codex", "gpt-5.4"},
		{"claude", "claude-sonnet-4-6"},
		{"gemini", "gemini-2.5-pro"},
	}
	alphaBranches := []string{
		"main", "feat/auth", "fix/parser",
	}
	betaBranches := []string{"main", "feat/api"}

	statuses := roborevStatusPattern(71)

	for i := range 71 {
		id := i + 1
		// Reserve IDs 70-73 for mutation fixtures
		if id >= 70 {
			break
		}

		repoID := 1
		branches := alphaBranches
		if id > 45 {
			repoID = 2
			branches = betaBranches
		}

		a := agents[i%3]
		branch := branches[i%len(branches)]
		jt := roborevJobType(i)
		s := statuses[i]

		if err := insertRoborevJob(
			tx, id, repoID, branch,
			a.name, a.model, s, jt, id,
		); err != nil {
			return err
		}
	}
	return nil
}

// roborevStatusPattern returns a deterministic status sequence.
// Distribution over 20-element cycle:
//
//	done(pass)=6, done(fail)=4, failed=2, canceled=2,
//	running=2, applied=1, rebased=1, queued=2
func roborevStatusPattern(n int) []string {
	cycle := []string{
		"done", "done", "done", "failed",
		"canceled", "done", "done", "running",
		"done", "canceled", "done", "applied",
		"done", "failed", "queued", "done",
		"done", "rebased", "running", "done",
	}
	out := make([]string, n)
	for i := range n {
		out[i] = cycle[i%len(cycle)]
	}
	return out
}

func roborevJobType(i int) string {
	switch i % 15 {
	case 3, 7:
		return "range"
	case 5:
		return "dirty"
	case 10:
		return "task"
	case 12:
		return "fix"
	case 14:
		return "compact"
	default:
		return "review"
	}
}

func insertRoborevJob(
	tx *sql.Tx, id, repoID int,
	branch, agent, model, status, jobType string,
	commitIdx int,
) error {
	enqueued := jobTime(id)
	gitRef := fmt.Sprintf("%08x", 0xaa000000+commitIdx)

	var commitID any = commitIdx
	if jobType == "range" {
		end := fmt.Sprintf("%08x", 0xaa000000+commitIdx+3)
		gitRef = gitRef + ".." + end
		commitID = nil
	}
	if jobType == "dirty" {
		gitRef = "dirty"
	}
	if jobType == "task" {
		gitRef = fmt.Sprintf("task-%d", id)
		commitID = nil
	}

	var startedAt, finishedAt, errMsg, tokenUsage any
	started := enqueued.Add(2 * time.Minute)
	finished := started.Add(5 * time.Minute)

	switch status {
	case "running":
		startedAt = started.Format(roborevTimeFmt)
	case "done", "applied", "rebased":
		startedAt = started.Format(roborevTimeFmt)
		finishedAt = finished.Format(roborevTimeFmt)
		if id%3 == 0 {
			tokenUsage = fmt.Sprintf(
				`{"input":%d,"output":%d}`,
				1200+id*10, 400+id*5)
		}
	case "failed":
		startedAt = started.Format(roborevTimeFmt)
		finishedAt = finished.Format(roborevTimeFmt)
		errMsg = "agent process exited with code 1"
	case "canceled":
		if id%2 == 0 {
			startedAt = started.Format(roborevTimeFmt)
		}
	}

	_, err := tx.Exec(
		`INSERT INTO review_jobs
		 (id, repo_id, commit_id, git_ref, branch,
		  agent, model, status, enqueued_at,
		  started_at, finished_at, error,
		  job_type, token_usage)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, repoID, commitID, gitRef, branch,
		agent, model, status,
		enqueued.Format(roborevTimeFmt),
		startedAt, finishedAt, errMsg, jobType, tokenUsage,
	)
	if err != nil {
		return fmt.Errorf("insert job %d: %w", id, err)
	}
	return nil
}

func seedRoborevMutationFixtures(tx *sql.Tx) error {
	// ID 70: queued job in alpha (cancel test)
	enq70 := jobTime(70)
	_, err := tx.Exec(
		`INSERT INTO review_jobs
		 (id, repo_id, commit_id, git_ref, branch,
		  agent, model, status, enqueued_at, job_type)
		 VALUES (70, 1, 70, ?, 'main', 'codex', 'gpt-5.4',
		         'queued', ?, 'review')`,
		fmt.Sprintf("%08x", 0xaa000000+70),
		enq70.Format(roborevTimeFmt),
	)
	if err != nil {
		return fmt.Errorf("insert mutation job 70: %w", err)
	}

	// ID 71: done/fail/open with review (close-review test)
	enq71 := jobTime(71)
	fin71 := enq71.Add(7 * time.Minute)
	_, err = tx.Exec(
		`INSERT INTO review_jobs
		 (id, repo_id, commit_id, git_ref, branch,
		  agent, model, status,
		  enqueued_at, started_at, finished_at, job_type)
		 VALUES (71, 1, 71, ?, 'feat/auth', 'claude',
		         'claude-sonnet-4-6', 'done', ?, ?, ?,
		         'review')`,
		fmt.Sprintf("%08x", 0xaa000000+71),
		enq71.Format(roborevTimeFmt),
		enq71.Add(2*time.Minute).Format(roborevTimeFmt),
		fin71.Format(roborevTimeFmt),
	)
	if err != nil {
		return fmt.Errorf("insert mutation job 71: %w", err)
	}

	// ID 72: done/fail/open with review + comment (add-comment)
	enq72 := jobTime(72)
	fin72 := enq72.Add(7 * time.Minute)
	_, err = tx.Exec(
		`INSERT INTO review_jobs
		 (id, repo_id, commit_id, git_ref, branch,
		  agent, model, status,
		  enqueued_at, started_at, finished_at, job_type)
		 VALUES (72, 1, 72, ?, 'fix/parser', 'gemini',
		         'gemini-2.5-pro', 'done', ?, ?, ?,
		         'review')`,
		fmt.Sprintf("%08x", 0xaa000000+72),
		enq72.Format(roborevTimeFmt),
		enq72.Add(2*time.Minute).Format(roborevTimeFmt),
		fin72.Format(roborevTimeFmt),
	)
	if err != nil {
		return fmt.Errorf("insert mutation job 72: %w", err)
	}

	// ID 73: failed job (rerun test) - no review
	enq73 := jobTime(73)
	fin73 := enq73.Add(7 * time.Minute)
	_, err = tx.Exec(
		`INSERT INTO review_jobs
		 (id, repo_id, commit_id, git_ref, branch,
		  agent, model, status,
		  enqueued_at, started_at, finished_at,
		  error, job_type)
		 VALUES (73, 1, 73, ?, 'main', 'codex', 'gpt-5.4',
		         'failed', ?, ?, ?,
		         'agent crashed during review', 'review')`,
		fmt.Sprintf("%08x", 0xaa000000+73),
		enq73.Format(roborevTimeFmt),
		enq73.Add(2*time.Minute).Format(roborevTimeFmt),
		fin73.Format(roborevTimeFmt),
	)
	if err != nil {
		return fmt.Errorf("insert mutation job 73: %w", err)
	}

	// ID 74: done, zero-duration (started_at == finished_at) so
	// elapsed displays as "0s". Ensures e2e coverage for the
	// "--" before "0s" sort boundary.
	enq74 := jobTime(74)
	started74 := enq74.Add(2 * time.Minute)
	_, err = tx.Exec(
		`INSERT INTO review_jobs
		 (id, repo_id, commit_id, git_ref, branch,
		  agent, model, status,
		  enqueued_at, started_at, finished_at, job_type,
		  token_usage)
		 VALUES (74, 2, 74, ?, 'feat/api', 'claude',
		         'claude-sonnet-4-6', 'done', ?, ?, ?,
		         'review', ?)`,
		fmt.Sprintf("%08x", 0xaa000000+74),
		enq74.Format(roborevTimeFmt),
		started74.Format(roborevTimeFmt),
		started74.Format(roborevTimeFmt),
		`{"total_output_tokens":28800,"peak_context_tokens":118000,"cost_usd":0.42,"has_cost":true}`,
	)
	if err != nil {
		return fmt.Errorf("insert mutation job 74: %w", err)
	}

	// Reviews for mutation jobs 71 and 72 (fail, open)
	for _, mj := range []struct {
		jobID int
		agent string
	}{
		{71, "claude"},
		{72, "gemini"},
	} {
		created := jobTime(mj.jobID).Add(8 * time.Minute)
		_, err = tx.Exec(
			`INSERT INTO reviews
			 (job_id, agent, prompt, output,
			  created_at, closed, verdict_bool)
			 VALUES (?, ?, 'Review commit', ?, ?, 0, 0)`,
			mj.jobID, mj.agent,
			roborevFailReview(mj.jobID),
			created.Format(roborevTimeFmt),
		)
		if err != nil {
			return fmt.Errorf(
				"insert mutation review for job %d: %w",
				mj.jobID, err)
		}
	}

	return nil
}

// seedRoborevReviews creates one review per done/applied/rebased job.
func seedRoborevReviews(tx *sql.Tx) error {
	// Skip mutation fixture IDs 71, 72 (reviews inserted separately)
	rows, err := tx.Query(
		`SELECT id, agent FROM review_jobs
		 WHERE status IN ('done', 'applied', 'rebased')
		   AND id NOT IN (71, 72)
		 ORDER BY id`,
	)
	if err != nil {
		return fmt.Errorf("query done jobs: %w", err)
	}
	defer rows.Close()

	type doneJob struct {
		id    int
		agent string
	}
	var jobs []doneJob
	for rows.Next() {
		var j doneJob
		if err := rows.Scan(&j.id, &j.agent); err != nil {
			return fmt.Errorf("scan done job: %w", err)
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate done jobs: %w", err)
	}

	passCount, failCount, closedCount := 0, 0, 0

	for i, j := range jobs {
		isPass := passCount < 20 &&
			(failCount >= 15 || i%3 != 2)
		if isPass {
			passCount++
		} else if failCount < 15 {
			failCount++
			isPass = false
		} else {
			isPass = true
			passCount++
		}

		var verdictBool int
		var output string
		if isPass {
			verdictBool = 1
			output = "No issues found. " +
				"Code follows project conventions."
		} else {
			verdictBool = 0
			output = roborevFailReview(j.id)
		}

		var closed int
		if closedCount < 5 && i%7 == 0 {
			closed = 1
			closedCount++
		}

		created := jobTime(j.id).Add(8 * time.Minute)
		_, err := tx.Exec(
			`INSERT INTO reviews
			 (job_id, agent, prompt, output,
			  created_at, closed, verdict_bool)
			 VALUES (?, ?, 'Review commit', ?, ?, ?, ?)`,
			j.id, j.agent, output,
			created.Format(roborevTimeFmt),
			closed, verdictBool,
		)
		if err != nil {
			return fmt.Errorf(
				"insert review for job %d: %w", j.id, err)
		}
	}

	return nil
}

func roborevFailReview(jobID int) string {
	var b strings.Builder
	b.WriteString("## Review Findings\n\n")
	fmt.Fprintf(&b,
		"### Finding 1: Error handling in function_%d\n\n",
		jobID)
	b.WriteString(
		"The error from `doWork()` is discarded silently. ")
	b.WriteString("Wrap and return:\n\n")
	b.WriteString("```go\nif err := doWork(); err != nil {\n")
	b.WriteString(
		"    return fmt.Errorf(\"do work: %w\", err)\n")
	b.WriteString("}\n```\n\n")
	fmt.Fprintf(&b,
		"### Finding 2: Missing test coverage (line %d)\n\n",
		100+jobID)
	b.WriteString(
		"The new branch is not covered by any test case.\n")
	return b.String()
}

func seedRoborevResponses(tx *sql.Tx) error {
	// Job 72 is a mutation fixture that must have a comment.
	commentJobs := []int{72}

	// Pick 2 more fail+open reviews from the bulk set.
	rows, err := tx.Query(
		`SELECT rj.id FROM review_jobs rj
		 JOIN reviews r ON r.job_id = rj.id
		 WHERE rj.status = 'done' AND r.verdict_bool = 0
		   AND r.closed = 0 AND rj.id NOT IN (70,71,72,73)
		 ORDER BY rj.id LIMIT 2`,
	)
	if err != nil {
		return fmt.Errorf("query comment targets: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("scan comment target: %w", err)
		}
		commentJobs = append(commentJobs, id)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate comment targets: %w", err)
	}

	responders := []string{"maintainer", "reviewer-bot"}
	comments := []string{
		"Agreed, this needs to be fixed before merge.",
		"I've filed a follow-up ticket for this.",
	}

	for i, jobID := range commentJobs {
		n := 1
		if i == 0 {
			n = 2 // first target gets 2 comments
		}
		for c := range n {
			created := jobTime(jobID).
				Add(time.Duration(12+c*3) * time.Minute)
			_, err := tx.Exec(
				`INSERT INTO responses
				 (job_id, responder, response, created_at)
				 VALUES (?, ?, ?, ?)`,
				jobID,
				responders[(i+c)%len(responders)],
				comments[(i+c)%len(comments)],
				created.Format(roborevTimeFmt),
			)
			if err != nil {
				return fmt.Errorf(
					"insert response for job %d: %w",
					jobID, err)
			}
		}
	}
	return nil
}

// jobTime returns a deterministic timestamp for the given job index,
// spread across 7 days (168 hours) from the base time.
func jobTime(i int) time.Time {
	hours := (i * 2) % 168
	return roborevBaseTime.Add(
		time.Duration(hours) * time.Hour)
}
