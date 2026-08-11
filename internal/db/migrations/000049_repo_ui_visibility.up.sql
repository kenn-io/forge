CREATE TABLE forge_hidden_repos (
    repo_id INTEGER PRIMARY KEY REFERENCES forge_repos(id) ON DELETE CASCADE
);
