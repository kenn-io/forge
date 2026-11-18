const repo = {
  provider: "github",
  platform_host: "github.com",
  platform_repo_id: 1007,
  owner: "org-a",
  name: "project-a",
  repo_path: "org-a/project-a",
};
export const activityTime = new Date().toISOString();

export const activitySubject = (number: number) => ({
  activity_at: activityTime,
  item_number: number,
  item_state: "open",
  item_title: `Pull request ${number}`,
  item_type: "pr",
  item_url: `https://github.com/org-a/project-a/pull/${number}`,
  repo,
});
