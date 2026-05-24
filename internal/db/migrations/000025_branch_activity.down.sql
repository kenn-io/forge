DROP INDEX IF EXISTS idx_branch_force_pushes_detected;
DROP INDEX IF EXISTS idx_branch_force_pushes_repo_detected;
DROP INDEX IF EXISTS idx_branch_force_pushes_dedupe;
DROP TABLE IF EXISTS middleman_branch_force_pushes;

DROP INDEX IF EXISTS idx_branch_tips_repo_branch;
DROP TABLE IF EXISTS middleman_branch_tips;

DROP INDEX IF EXISTS idx_branch_commits_committed;
DROP INDEX IF EXISTS idx_branch_commits_repo_committed;
DROP INDEX IF EXISTS idx_branch_commits_repo_branch_sha;
DROP INDEX IF EXISTS idx_branch_commits_repo_sha;
DROP TABLE IF EXISTS middleman_branch_commits;
