const GITLAB_RESOURCE_BOT_USERNAME = /^(?:project|group)_\d+_bot_[a-z0-9]+$/;

/** Whether a PR or issue author is a bot account on its provider. */
export function isBotAuthor(login: string, provider: string | undefined): boolean {
  const author = login.toLowerCase();
  if (author.endsWith("-bot")) return true;
  if (provider === "github") return author.endsWith("[bot]");
  if (provider === "gitlab") return GITLAB_RESOURCE_BOT_USERNAME.test(author);
  return false;
}
